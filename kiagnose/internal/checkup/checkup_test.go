/*
 * This file is part of the kiagnose project
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 * Copyright 2022 Red Hat, Inc.
 *
 */

package checkup_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	assert "github.com/stretchr/testify/require"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/kiagnose/kiagnose/kiagnose/internal/checkup"
	"github.com/kiagnose/kiagnose/kiagnose/internal/config"
)

const (
	namespaceResource          = "namespaces"
	namespaceKind              = "Namespace"
	clusterRoleBindingResource = "clusterrolebindings"
	clusterRoleBindingKind     = "ClusterRoleBinding"
	rolesResource              = "roles"
	rolesBindingResource       = "rolebindings"
	serviceAccountResource     = "serviceaccounts"
	configMapResource          = "configmaps"
	jobResource                = "jobs"

	testImage   = "framework:v1"
	testTimeout = time.Minute
)

type checkupSetupTestCase struct {
	description string
	clusterRole []*rbacv1.ClusterRole
	roles       []*rbacv1.Role
	envVars     []corev1.EnvVar
	resource    string
}

func TestCheckupWith(t *testing.T) {
	checkupCreateTestCases := []checkupSetupTestCase{
		{description: "no arguments"},
		{description: "ClusterRoles", clusterRole: newTestClusterRoles()},
		{description: "Roles", roles: newTestRoles()},
		{description: "env vars", envVars: newTestEnvVars()},
	}
	for _, testCase := range checkupCreateTestCases {
		t.Run(testCase.description, func(t *testing.T) {
			c := fake.NewSimpleClientset()
			testCheckup := checkup.New(c, &config.Config{
				Image:        testImage,
				Timeout:      testTimeout,
				EnvVars:      testCase.envVars,
				ClusterRoles: testCase.clusterRole,
				Roles:        testCase.roles,
			})

			assert.NoError(t, testCheckup.Setup())
			assertNamespaceCreated(t, c)
			assertServiceAccountCreated(t, c)
			assertResultsConfigMapCreated(t, c)
			assertConfigMapWriterRoleCreated(t, c)
			assertConfigMapWriterRoleBindingCreated(t, c)
			assertClusterRoleBindingsCreated(t, testsClient{c}, testCase.clusterRole)
		})
	}
}

func TestCheckupSetupShouldFailWhen(t *testing.T) {
	checkupCreateFailTestCases := []checkupSetupTestCase{
		{description: "Namespace creation failed", resource: namespaceResource},
		{description: "ServiceAccount creation failed", resource: serviceAccountResource},
		{description: "ConfigMap creation failed", resource: configMapResource},
		{description: "Role creation failed", resource: rolesResource},
		{description: "RolesBinding creation failed", resource: rolesBindingResource},
		{description: "ClusterRoleBinding creation failed",
			resource: clusterRoleBindingResource, clusterRole: newTestClusterRoles()},
	}
	for _, testCase := range checkupCreateFailTestCases {
		t.Run(testCase.description, func(t *testing.T) {
			testClient := newNormalizedFakeClientset()
			expectedErr := fmt.Sprintf("failed to create resource %q object", testCase.resource)
			testClient.injectCreateErrorForResource(testCase.resource, expectedErr)
			testCheckup := checkup.New(testClient, &config.Config{
				Image:        testImage,
				Timeout:      testTimeout,
				EnvVars:      testCase.envVars,
				ClusterRoles: testCase.clusterRole,
				Roles:        testCase.roles,
			})

			assert.ErrorContains(t, testCheckup.Setup(), expectedErr)

			assertNoObjectExists(t, testClient)
		})
	}
}

func TestCheckupSetupShould(t *testing.T) {
	t.Run("clean-up remaining ClusterRoleBinding on failure", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		expectedErr := fmt.Sprintf("failed to create resource %q object", clusterRoleBindingResource)
		expectedClusterRoles := newTestClusterRoles()
		testClient.injectClusterRoleBindingCreateError(expectedClusterRoles[1].Name, expectedErr)
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout, ClusterRoles: expectedClusterRoles})

		assert.ErrorContains(t, testCheckup.Setup(), expectedErr)

		assertNoObjectExists(t, testClient)
	})
}

func TestCheckupTeardownShould(t *testing.T) {
	t.Run("perform checkup teardown successfully", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout})

		assert.NoError(t, testCheckup.Setup())
		assert.NoError(t, testCheckup.Teardown())
	})

	t.Run("fail when failed to delete ClusterRoleBinding", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout, ClusterRoles: newTestClusterRoles()})

		assert.NoError(t, testCheckup.Setup())

		const expectedErr = "failed to delete ClusterRoleBinding"
		testClient.injectDeleteErrorForResource(clusterRoleBindingResource, expectedErr)

		assert.ErrorContains(t, testCheckup.Teardown(), expectedErr)
		assertNoNamespaceExists(t, testClient)
	})

	t.Run("fail when failed to delete Namespace", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout})

		assert.NoError(t, testCheckup.Setup())

		const expectedErr = "failed to delete ClusterRoleBinding"
		testClient.injectDeleteErrorForResource(namespaceResource, expectedErr)

		assert.ErrorContains(t, testCheckup.Teardown(), expectedErr)
		assertNoClusterRoleBindingExists(t, testClient)
	})

	t.Run("fail when Namespace wont dispose on time", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout})

		testCheckup.SetTeardownTimeout(time.Nanosecond)

		assert.NoError(t, testCheckup.Setup())

		const (
			getNamespaceError = "failed to get Namespace"
			expectedErrMatch  = "timed out"
		)
		testClient.injectGetErrorForResource(namespaceResource, getNamespaceError)

		assert.ErrorContains(t, testCheckup.Teardown(), expectedErrMatch)
		assertNoClusterRoleBindingExists(t, testClient)
	})

	t.Run("fail when ClusterRoleBinding wont dispose on time", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout, ClusterRoles: newTestClusterRoles()})

		testCheckup.SetTeardownTimeout(time.Nanosecond)

		assert.NoError(t, testCheckup.Setup())

		const (
			getClusterRoleBindingsError = "failed to get ClusterRoleBinding"
			expectedErrMatch            = "timed out"
		)
		testClient.injectGetErrorForResource(clusterRoleBindingResource, getClusterRoleBindingsError)

		assert.ErrorContains(t, testCheckup.Teardown(), expectedErrMatch)
		assertNoNamespaceExists(t, testClient)
	})

	t.Run("fail when failed to delete both Namespace and ClusterRoleBindings", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout, ClusterRoles: newTestClusterRoles()})

		assert.NoError(t, testCheckup.Setup())

		const (
			deleteNamespaceError          = "failed to delete Namespace"
			deleteClusterRoleBindingError = "failed to delete ClusterRoleBindings"
		)
		testClient.injectDeleteErrorForResource(namespaceResource, deleteNamespaceError)
		testClient.injectDeleteErrorForResource(clusterRoleBindingResource, deleteClusterRoleBindingError)

		err := testCheckup.Teardown()
		assert.ErrorContains(t, err, deleteNamespaceError)
		assert.ErrorContains(t, err, deleteClusterRoleBindingError)
	})
}

type checkupRunTestCase struct {
	description  string
	envVars      []corev1.EnvVar
	jobCondition *batchv1.JobCondition
}

func TestCheckupRunShouldCreateAJob(t *testing.T) {
	checkupRunTestCases := []checkupRunTestCase{
		{description: "with no checkup parameters"},
		{description: "with additional checkup parameters", envVars: newTestEnvVars()},
	}
	for _, testCase := range checkupRunTestCases {
		t.Run(testCase.description, func(t *testing.T) {
			testClient := newNormalizedFakeClientset()
			completeTrueJobCondition := &batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}
			testClient.injectJobWatchEvent(newJobWithCondition(checkup.NamespaceName, checkup.JobName, completeTrueJobCondition))
			testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout, EnvVars: testCase.envVars})

			assert.NoError(t, testCheckup.Setup())
			assert.NoError(t, testCheckup.Run())

			expectedEnvVars := []corev1.EnvVar{
				{Name: checkup.ResultsConfigMapNameEnvVarName, Value: checkup.ResultsConfigMapName},
				{Name: checkup.ResultsConfigMapNameEnvVarNamespace, Value: checkup.NamespaceName},
			}
			expectedEnvVars = append(expectedEnvVars, testCase.envVars...)
			expectedJob := checkup.NewCheckupJob(
				checkup.JobName, checkup.NamespaceName, checkup.ServiceAccountName, testImage, int64(testTimeout.Seconds()), expectedEnvVars)
			actualJob, err := testClient.BatchV1().Jobs(checkup.NamespaceName).Get(context.Background(), checkup.JobName, metav1.GetOptions{})
			assert.NoError(t, err)

			assert.Equal(t, actualJob, expectedJob)

			assert.NoError(t, testCheckup.Teardown())
			assertNoObjectExists(t, testClient)
		})
	}
}

func TestCheckupRunShouldSucceed(t *testing.T) {
	checkupRunTestCases := []checkupRunTestCase{
		{description: "when job is completed", jobCondition: &batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}},
		{description: "when job failed", jobCondition: &batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue}},
	}
	for _, testCase := range checkupRunTestCases {
		t.Run(testCase.description, func(t *testing.T) {
			testClient := newNormalizedFakeClientset()
			testClient.injectJobWatchEvent(newJobWithCondition(checkup.NamespaceName, checkup.JobName, testCase.jobCondition))
			testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout})

			assert.NoError(t, testCheckup.Setup())
			assert.NoError(t, testCheckup.Run())
			assert.NoError(t, testCheckup.Teardown())
			assertNoObjectExists(t, testClient)
		})
	}
}

func TestCheckupRunShouldFailWhen(t *testing.T) {
	t.Run("failed to create Job", func(t *testing.T) {
		const expectedErr = "failed to create Job"
		testClient := newNormalizedFakeClientset()
		testClient.injectCreateErrorForResource(jobResource, expectedErr)
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout})

		assert.NoError(t, testCheckup.Setup())
		assert.ErrorContains(t, testCheckup.Run(), expectedErr)
		assert.NoError(t, testCheckup.Teardown())
		assertNoObjectExists(t, testClient)
	})

	t.Run("fail to watch Job", func(t *testing.T) {
		const expectedErr = "failed to watch Job"
		testClient := newNormalizedFakeClientset()
		testClient.injectJobWatchError(expectedErr)
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: testTimeout})

		assert.NoError(t, testCheckup.Setup())
		assert.ErrorContains(t, testCheckup.Run(), expectedErr)
		assert.NoError(t, testCheckup.Teardown())
		assertNoObjectExists(t, testClient)
	})

	t.Run("Job wont finish on time", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: time.Nanosecond})

		assert.NoError(t, testCheckup.Setup())
		assert.ErrorContains(t, testCheckup.Run(), context.DeadlineExceeded.Error())
		assert.NoError(t, testCheckup.Teardown())
		assertNoObjectExists(t, testClient)
	})

	t.Run("Job wont finish on time with complete condition status false", func(t *testing.T) {
		testClient := newNormalizedFakeClientset()
		testCheckup := checkup.New(testClient, &config.Config{Image: testImage, Timeout: time.Second})
		completeFalseJobCondition := &batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionFalse}
		testClient.injectJobWatchEvent(newJobWithCondition(checkup.NamespaceName, checkup.JobName, completeFalseJobCondition))

		assert.NoError(t, testCheckup.Setup())
		assert.ErrorContains(t, testCheckup.Run(), context.DeadlineExceeded.Error())
		assert.NoError(t, testCheckup.Teardown())
		assertNoObjectExists(t, testClient)
	})
}

func newTestClusterRoles() []*rbacv1.ClusterRole {
	return []*rbacv1.ClusterRole{
		{TypeMeta: metav1.TypeMeta{Kind: "ClusterRole"}, ObjectMeta: metav1.ObjectMeta{Name: "cluster-role1"}},
		{TypeMeta: metav1.TypeMeta{Kind: "ClusterRole"}, ObjectMeta: metav1.ObjectMeta{Name: "cluster-role2"}}}
}

func newTestRoles() []*rbacv1.Role {
	return []*rbacv1.Role{
		{TypeMeta: metav1.TypeMeta{Kind: "Role"}, ObjectMeta: metav1.ObjectMeta{Name: "role1", Namespace: checkup.NamespaceName}},
		{TypeMeta: metav1.TypeMeta{Kind: "Role"}, ObjectMeta: metav1.ObjectMeta{Name: "role2", Namespace: checkup.NamespaceName}}}
}

func newTestEnvVars() []corev1.EnvVar {
	return []corev1.EnvVar{
		{Name: "env-var-1", Value: "env-var-1-value"},
		{Name: "env-var-1", Value: "env-var-1-value"}}
}

func newJobWithCondition(namespace, name string, condition *batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Status: batchv1.JobStatus{
			Conditions: []batchv1.JobCondition{*condition},
		},
	}
}

type testsClient struct{ *fake.Clientset }

// newNormalizedFakeClientset returns a new fake Kubernetes client with initialized empty lists
// for each group version resource used in the tests.
// Golang differentiates between an initialized slice/map that is empty and uninitialized one that is `nil`.
// By normalizing all to initialized & empty, the tests are extremely simplified, having no need to differentiate
// between such cases.
func newNormalizedFakeClientset() *testsClient {
	f := fake.NewSimpleClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "o"}},
		&rbacv1.ClusterRoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "o"}},
	)
	_ = f.CoreV1().Namespaces().Delete(context.Background(), "o", metav1.DeleteOptions{})
	_ = f.RbacV1().ClusterRoleBindings().Delete(context.Background(), "o", metav1.DeleteOptions{})

	return &testsClient{f}
}

func (c *testsClient) injectCreateErrorForResource(resourceName, err string) {
	const createVerb = "create"
	c.injectResourceManipulationError(createVerb, resourceName, err)
}

func (c *testsClient) injectGetErrorForResource(resourceName, err string) {
	const getVerb = "get"
	c.injectResourceManipulationError(getVerb, resourceName, err)
}

func (c *testsClient) injectDeleteErrorForResource(resourceName, err string) {
	const deleteVerb = "delete"
	c.injectResourceManipulationError(deleteVerb, resourceName, err)
}

func (c *testsClient) injectResourceManipulationError(verb, resourceName, err string) {
	reactionFn := func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New(err)
	}
	c.PrependReactor(verb, resourceName, reactionFn)
}

// injectClusterRoleBindingCreateError injects an error when the given ClusterRoleBinding name is created.
func (c *testsClient) injectClusterRoleBindingCreateError(clusterRoleBindingName, injectedErr string) {
	const createVerb = "create"
	reactionFn := func(action clienttesting.Action) (bool, runtime.Object, error) {
		if createAction, succeed := action.(clienttesting.CreateActionImpl); succeed {
			clusterRoleBinding, succeed := createAction.Object.(*rbacv1.ClusterRoleBinding)
			if succeed && clusterRoleBinding != nil &&
				clusterRoleBinding.Name == clusterRoleBindingName {
				return true, nil, errors.New(injectedErr)
			}
		}
		// delegate this action to the next reactor
		return false, nil, nil
	}
	c.PrependReactor(createVerb, clusterRoleBindingResource, reactionFn)
}

func (c *testsClient) injectJobWatchError(err string) {
	watchReactionFn := func(action clienttesting.Action) (bool, watch.Interface, error) {
		return true, nil, errors.New(err)
	}
	c.PrependWatchReactor(jobResource, watchReactionFn)
}

func (c *testsClient) injectJobWatchEvent(job *batchv1.Job) {
	watchReactionFn := func(action clienttesting.Action) (bool, watch.Interface, error) {
		watcher := watch.NewRaceFreeFake()
		watcher.Add(job)
		return true, watcher, nil
	}
	c.PrependWatchReactor(jobResource, watchReactionFn)
}

func (c *testsClient) listNamespaces() ([]corev1.Namespace, error) {
	objects, err := c.listObjectsByKind("", namespaceResource, namespaceKind)
	if err != nil {
		return nil, err
	}
	if objects != nil {
		namespacesList := objects.(*corev1.NamespaceList)
		return namespacesList.Items, nil
	}
	return nil, nil
}

func (c *testsClient) listClusterRoleBindings() ([]rbacv1.ClusterRoleBinding, error) {
	objects, err := c.listObjectsByKind(rbacv1.GroupName, clusterRoleBindingResource, clusterRoleBindingKind)
	if err != nil {
		return nil, err
	}
	if objects != nil {
		clusterRoleBindingsList := objects.(*rbacv1.ClusterRoleBindingList)
		return clusterRoleBindingsList.Items, nil
	}
	return nil, nil
}

func (c *testsClient) listObjectsByKind(group, resourceName, resourceKind string) (runtime.Object, error) {
	gvr := schema.GroupVersionResource{Group: group, Version: "v1", Resource: resourceName}
	gvk := schema.GroupVersionKind{Group: group, Version: "v1", Kind: resourceKind}
	return c.Tracker().List(gvr, gvk, "")
}

func assertNamespaceCreated(t *testing.T, testClient *fake.Clientset) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: namespaceResource}
	actualNs, err := testClient.Tracker().Get(gvr, "", checkup.NamespaceName)

	assert.NoError(t, err)
	assert.Equal(t, checkup.NewNamespace(checkup.NamespaceName), actualNs)
}

func assertServiceAccountCreated(t *testing.T, testClient *fake.Clientset) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: serviceAccountResource}
	actualServiceAccount, err := testClient.Tracker().Get(gvr, checkup.NamespaceName, checkup.ServiceAccountName)

	assert.NoError(t, err)
	assert.Equal(t, checkup.NewServiceAccount(checkup.ServiceAccountName, checkup.NamespaceName), actualServiceAccount)
}

func assertResultsConfigMapCreated(t *testing.T, testClient *fake.Clientset) {
	gvr := schema.GroupVersionResource{Group: "", Version: "v1", Resource: configMapResource}
	actualConfingMap, err := testClient.Tracker().Get(gvr, checkup.NamespaceName, checkup.ResultsConfigMapName)

	assert.NoError(t, err)
	assert.Equal(t, checkup.NewConfigMap(checkup.ResultsConfigMapName, checkup.NamespaceName), actualConfingMap)
}

func assertConfigMapWriterRoleCreated(t *testing.T, testClient *fake.Clientset) {
	gvr := schema.GroupVersionResource{Group: rbacv1.GroupName, Version: "v1", Resource: rolesResource}
	actualRole, err := testClient.Tracker().Get(gvr, checkup.NamespaceName, checkup.ResultsConfigMapWriterRoleName)

	assert.NoError(t, err)

	expectedRole := checkup.NewConfigMapWriterRole(
		checkup.ResultsConfigMapWriterRoleName, checkup.NamespaceName, checkup.ResultsConfigMapName)

	assert.Equal(t, expectedRole, actualRole)
}

func assertConfigMapWriterRoleBindingCreated(t *testing.T, testClient *fake.Clientset) {
	gvr := schema.GroupVersionResource{Group: rbacv1.GroupName, Version: "v1", Resource: rolesBindingResource}
	actualRoleBinding, err := testClient.Tracker().Get(gvr, checkup.NamespaceName, checkup.ResultsConfigMapWriterRoleName)

	assert.NoError(t, err)

	subject := rbacv1.Subject{Kind: rbacv1.ServiceAccountKind, Name: checkup.ServiceAccountName, Namespace: checkup.NamespaceName}
	expectedRoleBinding := checkup.NewRoleBinding(checkup.ResultsConfigMapWriterRoleName, checkup.NamespaceName, subject)

	assert.Equal(t, expectedRoleBinding, actualRoleBinding)
}

func assertClusterRoleBindingsCreated(t *testing.T, testClient testsClient, clusterRoles []*rbacv1.ClusterRole) {
	actualClusterRoleBindings, err := testClient.listClusterRoleBindings()
	assert.NoError(t, err)

	var expectedClusterRoleBindings []rbacv1.ClusterRoleBinding
	for _, clusterRoleBindingPtr := range checkup.NewClusterRoleBindings(clusterRoles, checkup.ServiceAccountName, checkup.NamespaceName) {
		expectedClusterRoleBindings = append(expectedClusterRoleBindings, *clusterRoleBindingPtr)
	}
	assert.Subset(t, actualClusterRoleBindings, expectedClusterRoleBindings)
}

// assertNoObjectExists checks that the checkup's Namespace and ClusterRoleBinding's are deleted.
// When a namespace is deleted each object inside it will be deleted eventually.
// The fake client won't perform forwarder checking when a Namespace is deleted and won't delete each
// associated object, Thus other objects that were created inside the checkup Namespace
// are not being checked explicitly.
func assertNoObjectExists(t *testing.T, testClient *testsClient) {
	assertNoNamespaceExists(t, testClient)
	assertNoClusterRoleBindingExists(t, testClient)
}

func assertNoNamespaceExists(t *testing.T, testClient *testsClient) {
	namespaces, err := testClient.listNamespaces()
	assert.NoError(t, err)
	assert.Empty(t, namespaces)
}

func assertNoClusterRoleBindingExists(t *testing.T, testClient *testsClient) {
	clusterRoleBindings, err := testClient.listClusterRoleBindings()
	assert.NoError(t, err)
	assert.Empty(t, clusterRoleBindings)
}

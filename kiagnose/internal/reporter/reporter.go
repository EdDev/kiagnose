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

package reporter

import (
	"errors"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/kiagnose/kiagnose/kiagnose/internal/configmap"
	"github.com/kiagnose/kiagnose/kiagnose/internal/status"
)

const (
	SucceededKey           = "status.succeeded"
	FailureReasonKey       = "status.failureReason"
	ResultsPrefix          = "status.result."
	StartTimestampKey      = "status.startTimestamp"
	CompletionTimestampKey = "status.completionTimestamp"
)

var ErrConfigMapDataIsNil = errors.New("configMap Data is nil")

type Reporter struct {
	client    kubernetes.Interface
	configMap *corev1.ConfigMap
}

func New(client kubernetes.Interface, configMapNamespace, configMapName string) *Reporter {
	return &Reporter{
		client: client,
		configMap: &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{
				Name:      configMapName,
				Namespace: configMapNamespace,
			},
		},
	}
}

func (r *Reporter) Report(statusData status.Status) error {
	if r.configMap.Data == nil {
		configMap, err := configmap.Get(r.client.CoreV1(), r.configMap.Namespace, r.configMap.Name)
		if err != nil {
			return err
		}

		r.configMap = configMap
	}

	if r.configMap.Data == nil {
		return ErrConfigMapDataIsNil
	}

	if !statusData.StartTimestamp.IsZero() {
		r.configMap.Data[StartTimestampKey] = statusData.StartTimestamp.Format(time.RFC3339)
	}

	if !statusData.CompletionTimestamp.IsZero() {
		r.configMap.Data[CompletionTimestampKey] = statusData.CompletionTimestamp.Format(time.RFC3339)
		r.configMap.Data[SucceededKey] = strconv.FormatBool(statusData.Succeeded)
		r.configMap.Data[FailureReasonKey] = strings.Join(statusData.FailureReason, ",")
	}

	for k, v := range statusData.Results {
		r.configMap.Data[ResultsPrefix+k] = v
	}

	updatedConfigMap, err := configmap.Update(r.client.CoreV1(), r.configMap)
	if err != nil {
		return err
	}

	r.configMap = updatedConfigMap

	return nil
}

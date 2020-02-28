/*
Copyright 2020 The Knative Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package resources

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"knative.dev/serving/pkg/apis/serving"
	v1 "knative.dev/serving/pkg/apis/serving/v1"
)

func TestEnvVar(t *testing.T) {
	tests := []struct {
		name string
		rev  *v1.Revision
		want []corev1.EnvVar
	}{{
		name: "revisions without objectMeta and labels",
		rev:  &v1.Revision{},
		want: []corev1.EnvVar{{
			Name: knativeRevisionEnvVariableKey,
		}, {
			Name: knativeConfigurationEnvVariableKey,
		}, {
			Name: knativeServiceEnvVariableKey,
		}, {
			Name: knativeNamespaceEnvVariableKey,
		}},
	}, {
		name: "revision without labels",
		rev: &v1.Revision{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bar-rev",
				Namespace: "foo",
			},
		},
		want: []corev1.EnvVar{{
			Name:  knativeRevisionEnvVariableKey,
			Value: "bar-rev",
		}, {
			Name: knativeConfigurationEnvVariableKey,
		}, {
			Name: knativeServiceEnvVariableKey,
		}, {
			Name:  knativeNamespaceEnvVariableKey,
			Value: "foo",
		}},
	}, {
		name: "revision with objectMeta and labels",
		rev: &v1.Revision{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "bar-rev",
				Namespace: "foo",
				Labels: map[string]string{
					serving.ConfigurationLabelKey: "bar",
					serving.ServiceLabelKey:       "bar",
				},
			},
		},
		want: []corev1.EnvVar{{
			Name:  knativeRevisionEnvVariableKey,
			Value: "bar-rev",
		}, {
			Name:  knativeConfigurationEnvVariableKey,
			Value: "bar",
		}, {
			Name:  knativeServiceEnvVariableKey,
			Value: "bar",
		}, {
			Name:  knativeNamespaceEnvVariableKey,
			Value: "foo",
		}},
	}}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if diff := cmp.Diff(test.want, getKnativeEnvVar(test.rev)); diff != "" {
				t.Errorf("environment variable (-want, +got) = %v", diff)
			}
		})
	}
}

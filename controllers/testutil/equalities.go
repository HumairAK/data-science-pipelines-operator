/*
Copyright 2023.

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

package testutil

import (
	"fmt"
	"github.com/go-test/deep"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/kubernetes/scheme"
)

// CompareResourceProcs maps object kind's to their associated comparison procedures.
// To add new comparison procedures, ensure they are added to compareResourceProcs
// and follow the same signature.
var CompareResourceProcs = map[string]func(expected, actual *unstructured.Unstructured) (bool, error){
	"Secret":     secretsAreEqual,
	"ConfigMap":  configMapsAreEqual,
	"Deployment": deploymentsAreEqual,
}

func configMapsAreEqual(expected, actual *unstructured.Unstructured) (bool, error) {
	expectedConfigMap := &v1.ConfigMap{}
	actualConfigMap := &v1.ConfigMap{}
	err := scheme.Scheme.Convert(expected, expectedConfigMap, nil)
	if err != nil {
		return false, err
	}
	err = scheme.Scheme.Convert(actual, actualConfigMap, nil)
	if err != nil {
		return false, err
	}

	if expectedConfigMap.Name != actualConfigMap.Name {
		return false, notEqualMsg("Configmap Names are not equal.")
	}

	diff := deep.Equal(expectedConfigMap.Data, actualConfigMap.Data)
	if diff != nil {
		return false, notDeeplyEqualMsg("Configmap's Data values", diff)
	}
	return true, nil
}

func secretsAreEqual(expected, actual *unstructured.Unstructured) (bool, error) {
	expectedSecret := &v1.Secret{}
	actualSecret := &v1.Secret{}
	err := scheme.Scheme.Convert(expected, expectedSecret, nil)
	if err != nil {
		return false, err
	}
	err = scheme.Scheme.Convert(actual, actualSecret, nil)
	if err != nil {
		return false, err
	}

	if expectedSecret.Name != actualSecret.Name {
		return false, notEqualMsg("Secret Names are not equal.")
	}

	diff := deep.Equal(expectedSecret.Data, actualSecret.Data)
	if diff != nil {
		return false, notDeeplyEqualMsg("Secret's Data values", diff)
	}
	return true, nil
}

func deploymentsAreEqual(expected, actual *unstructured.Unstructured) (bool, error) {
	expectedDep := &appsv1.Deployment{}
	actualDep := &appsv1.Deployment{}
	err := scheme.Scheme.Convert(expected, expectedDep, nil)
	if err != nil {
		return false, err
	}
	err = scheme.Scheme.Convert(actual, actualDep, nil)
	if err != nil {
		return false, err
	}
	diff := deep.Equal(expectedDep.ObjectMeta.Labels, actualDep.ObjectMeta.Labels)
	if diff != nil {
		return false, notDeeplyEqualMsg("labels", diff)
	}

	diff = deep.Equal(expectedDep.Spec.Selector, actualDep.Spec.Selector)
	if diff != nil {
		return false, notDeeplyEqualMsg("selector", diff)
	}

	diff = deep.Equal(expectedDep.Spec.Template.ObjectMeta, actualDep.Spec.Template.ObjectMeta)
	if diff != nil {
		return false, notDeeplyEqualMsg("spec template", diff)
	}

	diff = deep.Equal(expectedDep.Spec.Template.Spec.Volumes, actualDep.Spec.Template.Spec.Volumes)
	if diff != nil {
		return false, notDeeplyEqualMsg("Volumes", diff)
	}

	if len(expectedDep.Spec.Template.Spec.Containers) != len(actualDep.Spec.Template.Spec.Containers) {
		return false, notEqualMsg("Containers")
	}
	for i := range expectedDep.Spec.Template.Spec.Containers {
		expectedContainer := expectedDep.Spec.Template.Spec.Containers[i]
		actualContainer := actualDep.Spec.Template.Spec.Containers[i]

		if len(expectedContainer.Env) != len(actualContainer.Env) {
			return false, notEqualMsg("Container Env Lengths ")
		}
		// Check each env individually for a more meaningful response upon failure.
		for i, expectedEnv := range expectedContainer.Env {
			actualEnv := actualContainer.Env[i]
			diff = deep.Equal(expectedEnv, actualEnv)
			if diff != nil {
				return false, notDeeplyEqualMsg("Container Env", diff)
			}
		}

		diff = deep.Equal(expectedContainer.Ports, actualContainer.Ports)
		if diff != nil {
			return false, notDeeplyEqualMsg("Container Ports", diff)
		}
		diff = deep.Equal(expectedContainer.Resources, actualContainer.Resources)
		if diff != nil {
			return false, notDeeplyEqualMsg("Container Resources", diff)
		}
		diff = deep.Equal(expectedContainer.VolumeMounts, actualContainer.VolumeMounts)
		if diff != nil {
			return false, notDeeplyEqualMsg("Container VolumeMounts", diff)
		}
		diff = deep.Equal(expectedContainer.Args, actualContainer.Args)
		if diff != nil {
			return false, notDeeplyEqualMsg("Container Args", diff)
		}
		if expectedContainer.Name != actualContainer.Name {
			return false, notEqualMsg("Container Name")
		}
		if expectedContainer.Image != actualContainer.Image {
			return false, notEqualMsg(fmt.Sprintf("Container Image [expected: %s, actual: %s]", expectedContainer.Image, actualContainer.Image))
		}
	}

	return true, nil
}

func notEqualMsg(value string) error {
	return fmt.Errorf("%s are not equal", value)
}

func notDeeplyEqualMsg(value string, diff []string) error {
	errStr := fmt.Sprintf("%s is not equal:\n", value)
	for _, d := range diff {
		errStr += fmt.Sprintln("\t" + d)
	}
	return fmt.Errorf(errStr)
}

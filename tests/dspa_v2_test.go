//go:build test_integration

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

package integration

import (
	"fmt"
	"testing"

	systemsTestUtil "github.com/opendatahub-io/data-science-pipelines-operator/tests/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (suite *IntegrationTestSuite) TestDSPADeployment() {
	podCount := 8
	suite.T().Run("with default MariaDB and Minio", func(t *testing.T) {
		t.Run(fmt.Sprintf("should have %d pods", podCount), func(t *testing.T) {
			podList := &corev1.PodList{}
			listOpts := []client.ListOption{
				client.InNamespace(suite.DSPANamespace),
			}
			err := suite.Clientmgr.k8sClient.List(suite.Ctx, podList, listOpts...)
			require.NoError(t, err)
			assert.Equal(t, podCount, len(podList.Items))
		})

		deployments := []string{
			fmt.Sprintf("ds-pipeline-%s", suite.DSPA.Name),
			fmt.Sprintf("ds-pipeline-persistenceagent-%s", suite.DSPA.Name),
			fmt.Sprintf("ds-pipeline-scheduledworkflow-%s", suite.DSPA.Name),
			fmt.Sprintf("mariadb-%s", suite.DSPA.Name),
			fmt.Sprintf("minio-%s", suite.DSPA.Name),
		}

		for _, deployment := range deployments {
			t.Run(fmt.Sprintf("should have a ready %s deployment", deployment), func(t *testing.T) {
				systemsTestUtil.TestForSuccessfulDeployment(t, suite.Ctx, suite.DSPANamespace, deployment, suite.Clientmgr.k8sClient)
			})
		}
	})
}

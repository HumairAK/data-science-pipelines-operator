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

package controllers

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/go-logr/logr"
	mf "github.com/manifestival/manifestival"
	dspipelinesiov1alpha1 "github.com/opendatahub-io/data-science-pipelines-operator/api/v1alpha1"
	v1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"math/rand"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

const (
	defaultDBHostPrefix               = "mariadb"
	defaultDBHostPort                 = "3306"
	defaultDBUser                     = "mlpipeline"
	defaultDBName                     = "mlpipeline"
	defaultDBSecretKey                = "password"
	defaultMinioHostPrefix            = "minio"
	defaultMinioPort                  = "9000"
	defaultArtifactScriptConfigMap    = "ds-pipeline-artifact-script"
	defaultArtifactScriptConfigMapKey = "artifact_script"
	defaultDSPServicePrefix           = "ds-pipeline"
)

type DSPipelineParams struct {
	Name                 string
	Namespace            string
	Owner                mf.Owner
	APIServer            dspipelinesiov1alpha1.APIServer
	APIServerServiceName string
	ScheduledWorkflow    dspipelinesiov1alpha1.ScheduledWorkflow
	ViewerCRD            dspipelinesiov1alpha1.ViewerCRD
	PersistenceAgent     dspipelinesiov1alpha1.PersistenceAgent
	MlPipelineUI         dspipelinesiov1alpha1.MlPipelineUI
	MariaDB              dspipelinesiov1alpha1.MariaDB
	Minio                dspipelinesiov1alpha1.Minio
	DBConnection
	ObjectStorageConnection
}

type DBConnection struct {
	Host              string
	Port              string
	Username          string
	DBName            string
	CredentialsSecret dspipelinesiov1alpha1.SecretKeyValue
	Password          string
}

type ObjectStorageConnection struct {
	Bucket            string
	CredentialsSecret dspipelinesiov1alpha1.S3CredentialSecret
	AccessKeyID       string
	SecretAccessKey   string
	Secure            string
	Host              string
	Port              string
	Endpoint          string // protocol:host:port
}

func passwordGen(n int) string {
	rand.Seed(time.Now().UnixNano())
	var chars = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890")
	b := make([]rune, n)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

func (r *DSPipelineParams) UsingExternalDB(dsp *dspipelinesiov1alpha1.DSPipeline) (bool, error) {
	ExternalDBIsNotEmpty := !reflect.DeepEqual(dsp.Spec.Database.ExternalDB, dspipelinesiov1alpha1.ExternalDB{})
	MariaDBIsNotEmpty := !reflect.DeepEqual(dsp.Spec.Database.MariaDB, dspipelinesiov1alpha1.MariaDB{})
	if ExternalDBIsNotEmpty {
		return true, nil
	} else if MariaDBIsNotEmpty {
		return false, nil
	}
	return false, fmt.Errorf("no Database specified for DS-Pipeline resource")
}

func (r *DSPipelineParams) UsingExternalStorage(dsp *dspipelinesiov1alpha1.DSPipeline) (bool, error) {
	ExternalStorageIsNotEmpty := !reflect.DeepEqual(dsp.Spec.ObjectStorage.ExternalStorage, dspipelinesiov1alpha1.ExternalStorage{})
	MinioIsNotEmpty := !reflect.DeepEqual(dsp.Spec.ObjectStorage.Minio, dspipelinesiov1alpha1.Minio{})
	if ExternalStorageIsNotEmpty {
		return true, nil
	} else if MinioIsNotEmpty {
		return false, nil
	}
	return false, fmt.Errorf("no Database specified for DS-Pipeline resource")
}

func GetSecretDataDecoded(s *v1.Secret, key string) (string, error) {
	var secretData []byte
	_, err := base64.StdEncoding.Decode(secretData, s.Data[key])
	if err != nil {
		return "", err
	}
	return string(secretData), nil
}

func (r *DSPipelineParams) ExtractParams(ctx context.Context, dsp *dspipelinesiov1alpha1.DSPipeline,
	client client.Client, Log logr.Logger) error {
	r.Name = dsp.Name
	r.Namespace = dsp.Namespace
	r.Owner = dsp
	r.APIServer = dsp.Spec.APIServer
	r.APIServerServiceName = fmt.Sprintf("%s-%s", defaultDSPServicePrefix, r.Name)
	r.ScheduledWorkflow = dsp.Spec.ScheduledWorkflow
	r.ViewerCRD = dsp.Spec.ViewerCRD
	r.PersistenceAgent = dsp.Spec.PersistenceAgent
	r.MlPipelineUI = dsp.Spec.MlPipelineUI
	r.MariaDB = dsp.Spec.MariaDB
	r.Minio = dsp.Spec.Minio

	if dsp.Spec.APIServer.ArtifactScriptConfigMap == (dspipelinesiov1alpha1.ArtifactScriptConfigMap{}) {
		r.APIServer.ArtifactScriptConfigMap.Name = defaultArtifactScriptConfigMap
		r.APIServer.ArtifactScriptConfigMap.Key = defaultArtifactScriptConfigMapKey
	}

	usingExternalDB, err := r.UsingExternalDB(dsp)
	if err != nil {
		return err
	}

	if usingExternalDB {
		// Assume validation for CR ensures these values exist
		r.DBConnection.Host = dsp.Spec.ExternalDB.Host
		r.DBConnection.Port = dsp.Spec.ExternalDB.Port
		r.DBConnection.Username = dsp.Spec.ExternalDB.Username
		r.DBConnection.DBName = dsp.Spec.ExternalDB.DBName
		r.DBConnection.CredentialsSecret = dsp.Spec.ExternalDB.PasswordSecret

	} else {
		r.DBConnection.Host = fmt.Sprintf(
			"%s.%s.svc.cluster.local", defaultDBHostPrefix+"-"+r.Name,
			r.Namespace,
		)
		r.DBConnection.Port = defaultDBHostPort
		r.DBConnection.Username = defaultDBUser
		r.DBConnection.DBName = defaultDBName

		if dsp.Spec.ExternalDB.Username != "" {
			r.DBConnection.Username = dsp.Spec.ExternalDB.Username
		}
		if dsp.Spec.ExternalDB.DBName != "" {
			r.DBConnection.DBName = dsp.Spec.ExternalDB.DBName
		}
		mariaDBSecretSpecified := !reflect.DeepEqual(dsp.Spec.MariaDB.PasswordSecret, dspipelinesiov1alpha1.SecretKeyValue{})
		if mariaDBSecretSpecified {
			r.DBConnection.CredentialsSecret = dsp.Spec.MariaDB.PasswordSecret
		}
	}

	DBCredentialsNotSpecified := reflect.DeepEqual(r.DBConnection.CredentialsSecret, dspipelinesiov1alpha1.SecretKeyValue{})
	if DBCredentialsNotSpecified {
		// We assume validation ensures DB Credentials are specified for External DB
		// So this case is only possible if MariaDB deployment is specified, but no secret is provided.
		// In this case a custom secret will be created.
		r.DBConnection.CredentialsSecret = dspipelinesiov1alpha1.SecretKeyValue{
			Name: defaultDBHostPrefix + r.Name,
			Key:  defaultDBSecretKey,
		}
		r.DBConnection.Password = passwordGen(12)
	} else {
		// Attempt to fetch the specified DB secret
		dbSecret := &v1.Secret{}
		namespacedName := types.NamespacedName{
			Name:      r.DBConnection.CredentialsSecret.Name,
			Namespace: r.Namespace,
		}
		err = client.Get(ctx, namespacedName, dbSecret)
		if err != nil && apierrs.IsNotFound(err) {
			Log.Error(err, fmt.Sprintf("Specified DB secret %s does not exist.", r.DBConnection.CredentialsSecret.Name))
			return err
		} else if err != nil {
			Log.Error(err, "Unable to fetch DB secret...")
			return err
		}
		r.DBConnection.Password, err = GetSecretDataDecoded(dbSecret, r.DBConnection.CredentialsSecret.Key)
		if err != nil {
			Log.Error(err, fmt.Sprintf("Encountered error when trying to fetch key %s in secret %s",
				r.DBConnection.CredentialsSecret.Key, r.DBConnection.CredentialsSecret.Name))
			return err
		}
	}

	usingExternalStorage, err := r.UsingExternalStorage(dsp)
	if err != nil {
		return err
	}

	if usingExternalStorage {

	} else {

	}

	return nil
}

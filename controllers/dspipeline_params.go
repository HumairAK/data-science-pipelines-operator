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
	"errors"
	"fmt"
	"github.com/go-logr/logr"
	mf "github.com/manifestival/manifestival"
	dspipelinesiov1alpha1 "github.com/opendatahub-io/data-science-pipelines-operator/api/v1alpha1"
	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/config"
	v1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"math/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type DSPipelineParams struct {
	Name                 string
	Namespace            string
	Owner                mf.Owner
	APIServer            *dspipelinesiov1alpha1.APIServer
	APIServerServiceName string
	ScheduledWorkflow    *dspipelinesiov1alpha1.ScheduledWorkflow
	ViewerCRD            *dspipelinesiov1alpha1.ViewerCRD
	PersistenceAgent     *dspipelinesiov1alpha1.PersistenceAgent
	MlPipelineUI         *dspipelinesiov1alpha1.MlPipelineUI
	MariaDB              *dspipelinesiov1alpha1.MariaDB
	Minio                *dspipelinesiov1alpha1.Minio
	DBConnection
	ObjectStorageConnection
}

type DBConnection struct {
	Host              string
	Port              string
	Username          string
	DBName            string
	CredentialsSecret *dspipelinesiov1alpha1.SecretKeyValue
	Password          string
}

type ObjectStorageConnection struct {
	Bucket            string
	CredentialsSecret *dspipelinesiov1alpha1.S3CredentialSecret
	Secure            bool
	Host              string
	Port              string
	Scheme            string
	Endpoint          string // scheme://host:port
	AccessKeyID       string
	SecretAccessKey   string
}

// UsingExternalDB will return true if an external Database is specified in the CR, otherwise false.
func (p *DSPipelineParams) UsingExternalDB(dsp *dspipelinesiov1alpha1.DSPipeline) bool {
	if dsp.Spec.Database != nil && dsp.Spec.Database.ExternalDB != nil {
		return true
	}
	return false
}

// UsingExternalStorage will return true if an external Object Storage is specified in the CR, otherwise false.
func (p *DSPipelineParams) UsingExternalStorage(dsp *dspipelinesiov1alpha1.DSPipeline) bool {
	if dsp.Spec.ObjectStorage != nil && dsp.Spec.ObjectStorage.ExternalStorage != nil {
		return true
	}
	return false
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

// SetupDBParams Populates the DB connection Parameters.
// If an external secret is specified, SetupDBParams will retrieve DB credentials from it.
// If DSPO is managing a dynamically created secret, then SetupDBParams generates the creds.
func (p *DSPipelineParams) SetupDBParams(ctx context.Context, dsp *dspipelinesiov1alpha1.DSPipeline, client client.Client, log logr.Logger) error {

	usingExternalDB := p.UsingExternalDB(dsp)

	var customCreds *dspipelinesiov1alpha1.SecretKeyValue

	// Even if a secret is specified DSPO will deploy its own secret owned by DSPO
	p.DBConnection.CredentialsSecret = &dspipelinesiov1alpha1.SecretKeyValue{
		Name: config.MariaDBSecretNamePrefix + p.Name,
		Key:  config.MariaDBSecretKey,
	}

	if usingExternalDB {
		// Assume validation for CR ensures these values exist
		p.DBConnection.Host = dsp.Spec.Database.ExternalDB.Host
		p.DBConnection.Port = dsp.Spec.Database.ExternalDB.Port
		p.DBConnection.Username = dsp.Spec.Database.ExternalDB.Username
		p.DBConnection.DBName = dsp.Spec.Database.ExternalDB.DBName
		customCreds = dsp.Spec.Database.ExternalDB.PasswordSecret
	} else {

		mariaDBCfg := &dspipelinesiov1alpha1.MariaDB{}
		if dsp.Spec.Database != nil && dsp.Spec.Database.MariaDB != nil {
			mariaDBCfg = dsp.Spec.Database.MariaDB
		}

		p.DBConnection.Host = fmt.Sprintf(
			"%s.%s.svc.cluster.local",
			config.MariaDBHostPrefix+"-"+p.Name,
			p.Namespace,
		)
		p.DBConnection.Port = config.MariaDBHostPort
		p.DBConnection.Username = config.MariaDBUser
		p.DBConnection.DBName = config.MariaDBName

		if mariaDBCfg.Username != "" {
			p.DBConnection.Username = mariaDBCfg.Username
		}
		if mariaDBCfg.DBName != "" {
			p.DBConnection.DBName = mariaDBCfg.DBName
		}
		if mariaDBCfg.PasswordSecret != nil {
			customCreds = mariaDBCfg.PasswordSecret
		}
	}

	// Secret where DB credentials reside on cluster
	var credsSecretName string
	var credsPasswordKey string

	customCredentialsSpecified := customCreds != nil
	if customCredentialsSpecified {
		credsSecretName = customCreds.Name
		credsPasswordKey = customCreds.Key
	} else {
		credsSecretName = p.DBConnection.CredentialsSecret.Name
		credsPasswordKey = p.DBConnection.CredentialsSecret.Key
	}

	dbSecret := &v1.Secret{}
	namespacedName := types.NamespacedName{
		Name:      credsSecretName,
		Namespace: p.Namespace,
	}

	createNewSecret := false

	// Attempt to fetch the specified DB secret
	err := client.Get(ctx, namespacedName, dbSecret)
	if err != nil && apierrs.IsNotFound(err) {
		if !customCredentialsSpecified {
			generatedPass := passwordGen(12)
			p.DBConnection.Password = base64.StdEncoding.EncodeToString([]byte(generatedPass))
			createNewSecret = true
		} else {
			log.Error(err, fmt.Sprintf("DB secret [%s] was specified in CR but does not exist.",
				credsSecretName))
			return err
		}
	} else if err != nil {
		log.Error(err, "Unable to fetch DB secret...")
		return err
	}

	// Password was dynamically generated, no need to retrieve it from fetched secret
	if createNewSecret {
		return nil
	}

	p.DBConnection.Password = base64.StdEncoding.EncodeToString(dbSecret.Data[credsPasswordKey])

	if p.DBConnection.Password == "" {
		return errors.New(fmt.Sprintf("DB Password from secret [%s] for key [%s] was not successfully retrieved, "+
			"ensure that the secret with this key exist.", credsSecretName, credsPasswordKey))
	}
	return nil
}

// SetupObjectParams Populates the Object Storage connection Parameters.
// If an external secret is specified, SetupObjectParams will retrieve storage credentials from it.
// If DSPO is managing a dynamically created secret, then SetupObjectParams generates the creds.
func (p *DSPipelineParams) SetupObjectParams(ctx context.Context, dsp *dspipelinesiov1alpha1.DSPipeline, client client.Client, log logr.Logger) error {

	usingExternalObjectStorage := p.UsingExternalStorage(dsp)

	var customCreds *dspipelinesiov1alpha1.S3CredentialSecret

	// Even if a secret is specified DSPO will deploy its own secret owned by DSPO
	p.ObjectStorageConnection.CredentialsSecret = &dspipelinesiov1alpha1.S3CredentialSecret{
		SecretName: config.ObjectStorageSecretName,
		AccessKey:  config.ObjectStorageAccessKey,
		SecretKey:  config.ObjectStorageSecretKey,
	}
	p.ObjectStorageConnection.Secure = config.ObjectStoreConnectionSecure

	if usingExternalObjectStorage {
		// Assume validation for CR ensures these values exist
		p.ObjectStorageConnection.Bucket = dsp.Spec.ObjectStorage.ExternalStorage.Bucket
		customCreds = dsp.Spec.ObjectStorage.ExternalStorage.S3CredentialSecret
		p.ObjectStorageConnection.Host = dsp.Spec.ObjectStorage.ExternalStorage.Host
		p.ObjectStorageConnection.Port = dsp.Spec.ObjectStorage.ExternalStorage.Port
		p.ObjectStorageConnection.Scheme = dsp.Spec.ObjectStorage.ExternalStorage.Scheme
	} else {
		minioCfg := &dspipelinesiov1alpha1.Minio{}
		if dsp.Spec.ObjectStorage != nil && dsp.Spec.ObjectStorage.Minio != nil {
			minioCfg = dsp.Spec.ObjectStorage.Minio
		}

		p.ObjectStorageConnection.Bucket = config.MinioDefaultBucket
		p.ObjectStorageConnection.Host = fmt.Sprintf(
			"%s.%s.svc.cluster.local", config.MinioHostPrefix+"-"+p.Name,
			p.Namespace,
		)

		if minioCfg.Bucket != "" {
			p.ObjectStorageConnection.Bucket = minioCfg.Bucket
		}

		p.ObjectStorageConnection.Port = config.MinioPort
		p.ObjectStorageConnection.Scheme = config.MinioScheme
		if minioCfg.S3CredentialSecret != nil {
			customCreds = minioCfg.S3CredentialSecret
		}
	}

	p.ObjectStorageConnection.Endpoint = fmt.Sprintf(
		"%s://%s:%s",
		p.ObjectStorageConnection.Scheme,
		p.ObjectStorageConnection.Host,
		p.ObjectStorageConnection.Port,
	)

	// Secret where DB credentials reside on cluster
	var credsSecretName string
	var credsAccessKey string
	var credsSecretKey string

	customCredentialsSpecified := customCreds != nil
	if customCredentialsSpecified {
		credsSecretName = customCreds.SecretName
		credsAccessKey = customCreds.AccessKey
		credsSecretKey = customCreds.SecretKey
	} else {
		credsSecretName = p.ObjectStorageConnection.CredentialsSecret.SecretName
		credsAccessKey = p.ObjectStorageConnection.CredentialsSecret.AccessKey
		credsSecretKey = p.ObjectStorageConnection.CredentialsSecret.SecretKey
	}

	storageSecret := &v1.Secret{}
	namespacedName := types.NamespacedName{
		Name:      credsSecretName,
		Namespace: p.Namespace,
	}

	createNewSecret := false

	// Attempt to fetch the specified storage secret
	err := client.Get(ctx, namespacedName, storageSecret)
	if err != nil && apierrs.IsNotFound(err) {
		if !customCredentialsSpecified {
			generatedPass := passwordGen(16)
			p.ObjectStorageConnection.AccessKeyID = base64.StdEncoding.EncodeToString([]byte(generatedPass))
			generatedPass = passwordGen(24)
			p.ObjectStorageConnection.SecretAccessKey = base64.StdEncoding.EncodeToString([]byte(generatedPass))
			createNewSecret = true
		} else {
			log.Error(err, fmt.Sprintf("Storage secret %s was specified in CR but does not exist.",
				p.ObjectStorageConnection.CredentialsSecret.SecretName))
			return err
		}
	} else if err != nil {
		log.Error(err, "Unable to fetch Storage secret...")
		return err
	}

	// Password was dynamically generated, no need to retrieve it from fetched secret
	if createNewSecret {
		return nil
	}

	p.ObjectStorageConnection.AccessKeyID = base64.StdEncoding.EncodeToString(storageSecret.Data[credsAccessKey])
	p.ObjectStorageConnection.SecretAccessKey = base64.StdEncoding.EncodeToString(storageSecret.Data[credsSecretKey])

	if p.ObjectStorageConnection.AccessKeyID == "" || p.ObjectStorageConnection.SecretAccessKey == "" {
		return errors.New(fmt.Sprintf("Object Storage Password from secret [%s] for keys [%s, %s] was not "+
			"successfully retrieved, ensure that the secret with this key exist.", credsSecretName, credsAccessKey, credsSecretKey))
	}

	return nil
}

func setStringDefault(defaultValue string, value *string) {
	if *value == "" {
		*value = defaultValue
	}
}

func setResourcesDefault(defaultValue dspipelinesiov1alpha1.ResourceRequirements, value **dspipelinesiov1alpha1.ResourceRequirements) {
	if *value == nil {
		*value = &dspipelinesiov1alpha1.ResourceRequirements{
			Limits: &dspipelinesiov1alpha1.Resources{
				CPU:    defaultValue.Limits.CPU,
				Memory: defaultValue.Limits.Memory,
			},
			Requests: &dspipelinesiov1alpha1.Resources{
				CPU:    defaultValue.Requests.CPU,
				Memory: defaultValue.Requests.Memory,
			},
		}
	}
}

func (p *DSPipelineParams) ExtractParams(ctx context.Context, dsp *dspipelinesiov1alpha1.DSPipeline, client client.Client, log logr.Logger) error {
	p.Name = dsp.Name
	p.Namespace = dsp.Namespace
	p.Owner = dsp
	p.APIServer = dsp.Spec.APIServer
	p.APIServerServiceName = fmt.Sprintf("%s-%s", config.DSPServicePrefix, p.Name)
	p.ScheduledWorkflow = dsp.Spec.ScheduledWorkflow
	p.ViewerCRD = dsp.Spec.ViewerCRD
	p.PersistenceAgent = dsp.Spec.PersistenceAgent
	p.MlPipelineUI = dsp.Spec.MlPipelineUI
	p.MariaDB = dsp.Spec.MariaDB
	p.Minio = dsp.Spec.Minio

	// TODO: If p.<component> is nil we should create defaults

	if p.APIServer != nil {
		setStringDefault(config.APIServerImage, &p.APIServer.Image)
		setStringDefault(config.APIServerArtifactImage, &p.APIServer.ArtifactImage)
		setStringDefault(config.APIServerCacheImage, &p.APIServer.CacheImage)
		setStringDefault(config.APIServerMoveResultsImage, &p.APIServer.MoveResultsImage)
		setResourcesDefault(config.APIServerResourceRequirements, &p.APIServer.Resources)

		if p.APIServer.ArtifactScriptConfigMap != nil {
			p.APIServer.ArtifactScriptConfigMap.Name = config.ArtifactScriptConfigMap
			p.APIServer.ArtifactScriptConfigMap.Key = config.ArtifactScriptConfigMapKey
		}
	}

	if p.PersistenceAgent != nil {
		setStringDefault(config.PersistenceAgentImage, &p.PersistenceAgent.Image)
		setResourcesDefault(config.PersistenceAgentResourceRequirements, &p.PersistenceAgent.Resources)
	}
	if p.ScheduledWorkflow != nil {
		setStringDefault(config.ScheduledWorkflowImage, &p.ScheduledWorkflow.Image)
		setResourcesDefault(config.ScheduledWorkflowResourceRequirements, &p.ScheduledWorkflow.Resources)
	}
	if p.ViewerCRD != nil {
		setStringDefault(config.ViewerCRDImage, &p.ViewerCRD.Image)
		setResourcesDefault(config.ViewerCRDResourceRequirements, &p.ViewerCRD.Resources)
	}
	if p.MariaDB != nil {
		setStringDefault(config.MariaDBImage, &p.MariaDB.Image)
		setResourcesDefault(config.MariaDBResourceRequirements, &p.MariaDB.Resources)
	}
	if p.Minio != nil {
		setStringDefault(config.MinioImage, &p.Minio.Image)
		setResourcesDefault(config.MinioResourceRequirements, &p.Minio.Resources)
	}

	if p.MlPipelineUI != nil {
		setStringDefault(config.MlPipelineUIImage, &p.MlPipelineUI.Image)
		setResourcesDefault(config.MlPipelineUIResourceRequirements, &p.MlPipelineUI.Resources)
	}

	err := p.SetupDBParams(ctx, dsp, client, log)
	if err != nil {
		return err
	}

	err = p.SetupObjectParams(ctx, dsp, client, log)
	if err != nil {
		return err
	}

	return nil
}

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
	dspa "github.com/opendatahub-io/data-science-pipelines-operator/api/v1alpha1"
	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/config"
	v1 "k8s.io/api/core/v1"
	apierrs "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"math/rand"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"time"
)

type DSPAParams struct {
	Name                               string
	Namespace                          string
	Owner                              mf.Owner
	APIServer                          *dspa.APIServer
	APIServerServiceName               string
	APIServerOauthProxyCookieSecret    string
	ScheduledWorkflow                  *dspa.ScheduledWorkflow
	ViewerCRD                          *dspa.ViewerCRD
	PersistenceAgent                   *dspa.PersistenceAgent
	MlPipelineUI                       *dspa.MlPipelineUI
	MlPipelineUIOauthProxyCookieSecret string
	MariaDB                            *dspa.MariaDB
	Minio                              *dspa.Minio
	DBConnection
	ObjectStorageConnection
}

type DBConnection struct {
	Host              string
	Port              string
	Username          string
	DBName            string
	CredentialsSecret *dspa.SecretKeyValue
	Password          string
}

type ObjectStorageConnection struct {
	Bucket            string
	CredentialsSecret *dspa.S3CredentialSecret
	Secure            bool
	Host              string
	Port              string
	Scheme            string
	Endpoint          string // scheme://host:port
	AccessKeyID       string
	SecretAccessKey   string
}

// UsingExternalDB will return true if an external Database is specified in the CR, otherwise false.
func (p *DSPAParams) UsingExternalDB(dsp *dspa.DataSciencePipelinesApplication) bool {
	if dsp.Spec.Database != nil && dsp.Spec.Database.ExternalDB != nil {
		return true
	}
	return false
}

// UsingExternalStorage will return true if an external Object Storage is specified in the CR, otherwise false.
func (p *DSPAParams) UsingExternalStorage(dsp *dspa.DataSciencePipelinesApplication) bool {
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
func (p *DSPAParams) SetupDBParams(ctx context.Context, dsp *dspa.DataSciencePipelinesApplication, client client.Client, log logr.Logger) error {

	usingExternalDB := p.UsingExternalDB(dsp)

	var customCreds *dspa.SecretKeyValue

	// Even if a secret is specified DSPO will deploy its own secret owned by DSPO
	p.DBConnection.CredentialsSecret = &dspa.SecretKeyValue{
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

		// If no externalDB or mariaDB is specified, DSPO assumes
		// MariaDB deployment with defaults.
		if p.MariaDB == nil {
			p.MariaDB = &dspa.MariaDB{
				Deploy:    true,
				Image:     config.GetStringConfigWithDefault(config.MariaDBImagePath, config.DefaultImageValue),
				Resources: config.MariaDBResourceRequirements.DeepCopy(),
				Username:  config.MariaDBUser,
				DBName:    config.MariaDBName,
				PVCSize:   config.MariaDBNamePVCSize,
			}
		}
		// If MariaDB was specified, ensure missing fields are
		// populated with defaults.
		if p.MariaDB.Image == "" {
			p.MariaDB.Image = config.GetStringConfigWithDefault(config.MariaDBImagePath, config.DefaultImageValue)
		}
		setStringDefault(config.MariaDBUser, &p.MariaDB.Username)
		setStringDefault(config.MariaDBName, &p.MariaDB.DBName)
		setResourcesDefault(config.MariaDBResourceRequirements, &p.MariaDB.Resources)

		p.DBConnection.Host = fmt.Sprintf(
			"%s.%s.svc.cluster.local",
			config.MariaDBHostPrefix+"-"+p.Name,
			p.Namespace,
		)
		p.DBConnection.Port = config.MariaDBHostPort
		p.DBConnection.Username = p.MariaDB.Username
		p.DBConnection.DBName = p.MariaDB.DBName
		if p.MariaDB.PasswordSecret != nil {
			customCreds = p.MariaDB.PasswordSecret
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
		return fmt.Errorf(fmt.Sprintf("DB Password from secret [%s] for key [%s] was not successfully retrieved, "+
			"ensure that the secret with this key exist.", credsSecretName, credsPasswordKey))
	}
	return nil
}

// SetupObjectParams Populates the Object Storage connection Parameters.
// If an external secret is specified, SetupObjectParams will retrieve storage credentials from it.
// If DSPO is managing a dynamically created secret, then SetupObjectParams generates the creds.
func (p *DSPAParams) SetupObjectParams(ctx context.Context, dsp *dspa.DataSciencePipelinesApplication, client client.Client, log logr.Logger) error {

	usingExternalObjectStorage := p.UsingExternalStorage(dsp)

	var customCreds *dspa.S3CredentialSecret

	// Even if a secret is specified DSPO will deploy its own secret owned by DSPO
	p.ObjectStorageConnection.CredentialsSecret = &dspa.S3CredentialSecret{
		SecretName: config.ObjectStorageSecretName,
		AccessKey:  config.ObjectStorageAccessKey,
		SecretKey:  config.ObjectStorageSecretKey,
	}
	p.ObjectStorageConnection.Secure = config.ObjectStoreConnectionSecure

	if usingExternalObjectStorage {
		// Assume validation for CR ensures these values exist
		p.ObjectStorageConnection.Bucket = dsp.Spec.ObjectStorage.ExternalStorage.Bucket
		p.ObjectStorageConnection.Host = dsp.Spec.ObjectStorage.ExternalStorage.Host
		p.ObjectStorageConnection.Port = dsp.Spec.ObjectStorage.ExternalStorage.Port
		p.ObjectStorageConnection.Scheme = dsp.Spec.ObjectStorage.ExternalStorage.Scheme
		customCreds = dsp.Spec.ObjectStorage.ExternalStorage.S3CredentialSecret
	} else {
		// If no ExternalStorage or Minio is specified, DSPO assumes
		// Minio deployment with defaults.
		if p.Minio == nil {
			p.Minio = &dspa.Minio{
				Deploy:    true,
				Image:     config.GetStringConfigWithDefault(config.MinioImagePath, config.DefaultImageValue),
				Bucket:    config.MinioDefaultBucket,
				PVCSize:   config.MinioPVCSize,
				Resources: config.MinioResourceRequirements.DeepCopy(),
			}
		}

		// If Minio was specified, ensure missing fields are
		// populated with defaults.
		if p.Minio.Image == "" {
			p.Minio.Image = config.GetStringConfigWithDefault(config.MinioImagePath, config.DefaultImageValue)
		}

		setStringDefault(config.MinioDefaultBucket, &p.Minio.Bucket)
		setStringDefault(config.MinioPVCSize, &p.Minio.PVCSize)
		setResourcesDefault(config.MinioResourceRequirements, &p.Minio.Resources)

		p.ObjectStorageConnection.Bucket = config.MinioDefaultBucket
		p.ObjectStorageConnection.Host = fmt.Sprintf(
			"%s.%s.svc.cluster.local",
			config.MinioHostPrefix+"-"+p.Name,
			p.Namespace,
		)
		p.ObjectStorageConnection.Port = config.MinioPort
		p.ObjectStorageConnection.Scheme = config.MinioScheme
		if p.Minio.S3CredentialSecret != nil {
			customCreds = p.Minio.S3CredentialSecret
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
		return fmt.Errorf(fmt.Sprintf("Object Storage Password from secret [%s] for keys [%s, %s] was not "+
			"successfully retrieved, ensure that the secret with this key exist.", credsSecretName, credsAccessKey, credsSecretKey))
	}

	return nil
}

func setStringDefault(defaultValue string, value *string) {
	if *value == "" {
		*value = defaultValue
	}
}

func setResourcesDefault(defaultValue dspa.ResourceRequirements, value **dspa.ResourceRequirements) {
	if *value == nil {
		*value = defaultValue.DeepCopy()
	}
}

func (p *DSPAParams) ExtractParams(ctx context.Context, dsp *dspa.DataSciencePipelinesApplication, client client.Client, logger logr.Logger) error {
	log := logger.WithValues("namespace", dsp.Namespace)

	p.Name = dsp.Name
	p.Namespace = dsp.Namespace
	p.Owner = dsp
	p.APIServer = dsp.Spec.APIServer.DeepCopy()
	p.APIServerServiceName = fmt.Sprintf("%s-%s", config.DSPServicePrefix, p.Name)
	p.ScheduledWorkflow = dsp.Spec.ScheduledWorkflow.DeepCopy()
	p.ViewerCRD = dsp.Spec.ViewerCRD.DeepCopy()
	p.PersistenceAgent = dsp.Spec.PersistenceAgent.DeepCopy()
	p.MlPipelineUI = dsp.Spec.MlPipelineUI.DeepCopy()
	p.MariaDB = dsp.Spec.MariaDB.DeepCopy()
	p.Minio = dsp.Spec.Minio.DeepCopy()

	// TODO: If p.<component> is nil we should create defaults

	if p.APIServer != nil {

		p.APIServer.Image = config.GetStringConfigWithDefault(config.APIServerImagePath, config.DefaultImageValue)
		p.APIServer.ArtifactImage = config.GetStringConfigWithDefault(config.APIServerArtifactImagePath, config.DefaultImageValue)
		p.APIServer.CacheImage = config.GetStringConfigWithDefault(config.APIServerCacheImagePath, config.DefaultImageValue)
		p.APIServer.MoveResultsImage = config.GetStringConfigWithDefault(config.APIServerMoveResultsImagePath, config.DefaultImageValue)

		setResourcesDefault(config.APIServerResourceRequirements, &p.APIServer.Resources)

		generatedPass := passwordGen(12)
		p.APIServerOauthProxyCookieSecret = base64.StdEncoding.EncodeToString([]byte(generatedPass))

		if p.APIServer.ArtifactScriptConfigMap == nil {
			p.APIServer.ArtifactScriptConfigMap = &dspa.ArtifactScriptConfigMap{
				Name: config.ArtifactScriptConfigMapNamePrefix + dsp.Name,
				Key:  config.ArtifactScriptConfigMapKey,
			}
		}
	}
	if p.PersistenceAgent != nil {
		p.PersistenceAgent.Image = config.GetStringConfigWithDefault(config.PersistenceAgentImagePath, config.DefaultImageValue)
		setResourcesDefault(config.PersistenceAgentResourceRequirements, &p.PersistenceAgent.Resources)
	}
	if p.ScheduledWorkflow != nil {
		p.ScheduledWorkflow.Image = config.GetStringConfigWithDefault(config.ScheduledWorkflowImagePath, config.DefaultImageValue)
		setResourcesDefault(config.ScheduledWorkflowResourceRequirements, &p.ScheduledWorkflow.Resources)
	}
	if p.ViewerCRD != nil {
		p.ViewerCRD.Image = config.GetStringConfigWithDefault(config.ViewerCRDImagePath, config.DefaultImageValue)
		setResourcesDefault(config.ViewerCRDResourceRequirements, &p.ViewerCRD.Resources)
	}
	if p.MlPipelineUI != nil {
		p.MlPipelineUI.Image = config.GetStringConfigWithDefault(config.MlPipelineUIImagePath, config.DefaultImageValue)
		setStringDefault(config.MLPipelineUIConfigMapPrefix+dsp.Name, &p.MlPipelineUI.ConfigMapName)
		setResourcesDefault(config.MlPipelineUIResourceRequirements, &p.MlPipelineUI.Resources)

		generatedPass := passwordGen(12)
		p.MlPipelineUIOauthProxyCookieSecret = base64.StdEncoding.EncodeToString([]byte(generatedPass))
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

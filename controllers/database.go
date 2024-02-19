/*

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
	cryptoTls "crypto/tls"
	"database/sql"
	b64 "encoding/base64"
	"fmt"
	"github.com/go-logr/logr"
	"github.com/go-sql-driver/mysql"
	_ "github.com/go-sql-driver/mysql"
	dspav1alpha1 "github.com/opendatahub-io/data-science-pipelines-operator/api/v1alpha1"
	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/config"
	"github.com/opendatahub-io/data-science-pipelines-operator/controllers/util"
	"k8s.io/apimachinery/pkg/util/json"
)

const dbSecret = "mariadb/secret.yaml.tmpl"

var dbTemplates = []string{
	"mariadb/deployment.yaml.tmpl",
	"mariadb/pvc.yaml.tmpl",
	"mariadb/service.yaml.tmpl",
	"mariadb/mariadb-sa.yaml.tmpl",
	dbSecret,
}

var ConnectAndQueryDatabase = func(host string, log logr.Logger, port, username, password, dbname string, tls string, pemCerts []byte) (bool, error) {
	// Create a context with a timeout of 1 second
	ctx, cancel := context.WithTimeout(context.Background(), config.DefaultDBConnectionTimeout)
	defer cancel()

	var tlsConfig *cryptoTls.Config
	var tlsConnectionSuffix = ""
	switch tls {
	case "false", "":
		// don't set anything
		break
	case "true":
		if len(pemCerts) != 0 {
			tr, err := util.GetHttpsTransportWithCACert(log, pemCerts)
			if err != nil {
				log.Error(err, "Encountered error when processing custom ca bundle.")
				return false, err
			}
			tlsConfig = tr.TLSClientConfig
		}
		err := mysql.RegisterTLSConfig("custom", tlsConfig)
		if err != nil {
			return false, err
		}
		tlsConnectionSuffix = fmt.Sprintf("?tls=%s", tls)
		break
	case "skip-verify", "preferred":
		tlsConfig = &cryptoTls.Config{InsecureSkipVerify: true}
		tlsConnectionSuffix = fmt.Sprintf("?tls=%s", tls)
		break
	default:
		// Unknown config, default to don't set anything
		break
	}

	connectionString := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s%s", username, password, host, port, dbname, tlsConnectionSuffix)
	db, err := sql.Open("mysql", connectionString)
	if err != nil {
		return false, err
	}
	defer db.Close()

	// If external db, and custom params set and tls: false, not tls enabled
	// if external db, and custom params set to skip-verify or preferred, then skip-verify

	testStatement := "SELECT 1;"
	_, err = db.QueryContext(ctx, testStatement)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *DSPAReconciler) isDatabaseAccessible(ctx context.Context, dsp *dspav1alpha1.DataSciencePipelinesApplication,
	params *DSPAParams) bool {
	log := r.Log.WithValues("namespace", dsp.Namespace).WithValues("dspa_name", dsp.Name)

	if params.DatabaseHealthCheckDisabled(dsp) {
		log.V(1).Info("Database health check disabled, assuming database is available and ready.")
		return true
	}

	log.Info("Performing Database Health Check")
	databaseSpecified := dsp.Spec.Database != nil
	usingExternalDB := params.UsingExternalDB(dsp)
	usingMariaDB := !databaseSpecified || dsp.Spec.Database.MariaDB != nil
	if !usingMariaDB && !usingExternalDB {
		log.Info("Could not connect to Database: Unsupported Type")
		return false
	}

	decodePass, _ := b64.StdEncoding.DecodeString(params.DBConnection.Password)

	var extraParamsJson map[string]string
	err := json.Unmarshal([]byte(params.DBConnection.ExtraParams), &extraParamsJson)
	if err != nil {
		log.Info(fmt.Sprintf("Could not parse tls config in ExtraParams, if setting CustomExtraParams, ensure the JSON string is well-formed. Error: %v", err))
		return false
	}

	// tls can be true, false, skip-verify, preferred
	// we default to true if it's an externalDB, false otherwise (if not specified via CustomExtraParams)
	tls := "false"
	if usingExternalDB {
		tls = "true"
	}

	// Override tls with the value in ExtraParams, if specified
	// If users have specified a CustomExtraParams field, they may choose to leave out the "tls" param
	if val, ok := extraParamsJson["tls"]; ok {
		tls = val
	}

	dbHealthCheckPassed, err := ConnectAndQueryDatabase(
		params.DBConnection.Host,
		log,
		params.DBConnection.Port,
		params.DBConnection.Username,
		string(decodePass),
		params.DBConnection.DBName,
		tls,
		params.APICustomPemCerts)

	if err != nil {
		log.Info(fmt.Sprintf("Unable to connect to Database: %v", err))
		return false
	}

	if dbHealthCheckPassed {
		log.Info("Database Health Check Successful")
	}

	return dbHealthCheckPassed
}

func (r *DSPAReconciler) ReconcileDatabase(ctx context.Context, dsp *dspav1alpha1.DataSciencePipelinesApplication,
	params *DSPAParams) error {

	log := r.Log.WithValues("namespace", dsp.Namespace).WithValues("dspa_name", dsp.Name)
	databaseSpecified := dsp.Spec.Database != nil
	// DB field can be specified as an empty obj, confirm that subfields are also specified
	// By default if Database is empty, we deploy mariadb
	externalDBSpecified := params.UsingExternalDB(dsp)
	mariaDBSpecified := dsp.Spec.Database.MariaDB != nil
	defaultDBRequired := !databaseSpecified || (!externalDBSpecified && !mariaDBSpecified)

	deployMariaDB := mariaDBSpecified && dsp.Spec.Database.MariaDB.Deploy
	// Default DB is currently MariaDB as well, but storing these bools seperately in case that changes
	deployDefaultDB := !databaseSpecified || defaultDBRequired

	// If external db is specified, it takes precedence
	if externalDBSpecified {
		log.Info("Deploying external db secret.")
		// If using external DB, we just need to create the secret
		// for apiserver
		err := r.Apply(dsp, params, dbSecret)
		if err != nil {
			return err
		}
	} else if deployMariaDB || deployDefaultDB {
		log.Info("Applying mariaDB resources.")
		for _, template := range dbTemplates {
			err := r.Apply(dsp, params, template)
			if err != nil {
				return err
			}
		}
		// If no database was not specified, deploy mariaDB by default.
		// Update the CR with the state of mariaDB to accurately portray
		// desired state.
		if !databaseSpecified {
			dsp.Spec.Database = &dspav1alpha1.Database{}
		}
		if !databaseSpecified || defaultDBRequired {
			dsp.Spec.Database.MariaDB = params.MariaDB.DeepCopy()
			dsp.Spec.Database.MariaDB.Deploy = true
			if err := r.Update(ctx, dsp); err != nil {
				return err
			}
		}
	} else {
		log.Info("No externalDB detected, and mariaDB disabled. " +
			"skipping Application of DB Resources")
		return nil
	}
	log.Info("Finished applying Database Resources")

	return nil
}

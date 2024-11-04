package gce

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"

	gcfg "gopkg.in/gcfg.v1"
	cloudgce "k8s.io/cloud-provider-gcp/providers/gce"
	"k8s.io/ingress-gce/cmd/glbc/app"
	"k8s.io/ingress-gce/pkg/flags"
	"k8s.io/ingress-gce/pkg/multiproject/projectcrd"
	"k8s.io/klog/v2"
)

// NewGCEForProject returns a new GCE client for the given project.
// If the projectCRD is nil, it returns the default cloud, associated with the cluster-project.
func NewGCEForProject(defaultConfigFile cloudgce.ConfigFile, projectCRD *projectcrd.Project, logger klog.Logger) (*cloudgce.Cloud, error) {
	configFile, err := generateConfigFileForProject(defaultConfigFile, projectCRD)
	if err != nil {
		return nil, fmt.Errorf("failed to generate config file for project %s: %v", projectCRD.ProjectName(), err)
	}

	// convert file struct to bytes
	configBytes, err := json.Marshal(configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal config file: %v", err)
	}

	configReader := func() io.Reader {
		return bytes.NewReader(configBytes)
	}

	return app.GCEClientForConfigReader(configReader, logger), nil
}

// generateConfigFileForProject generates a new GCE config file for the given project.
// It returns the default config file if the project name is not set, which is considered as the cluster-project.
// Otherwise, it replaces needed fields in the config file with the values from the project CRD.
func generateConfigFileForProject(defaultConfigFile cloudgce.ConfigFile, projectCRD *projectcrd.Project) (*cloudgce.ConfigFile, error) {
	if projectCRD == nil {
		return &defaultConfigFile, nil
	}

	configFile := &defaultConfigFile

	configFile.Global.ProjectID = projectCRD.ProjectID()
	projectNumber := projectCRD.ProjectNumber()
	configFile.Global.TokenURL = replaceTokenURLProjectNumber(configFile.Global.TokenURL, fmt.Sprintf("%d", projectNumber))

	// Update the TokenBody with the new project number
	newTokenBody, err := replaceTokenBodyProjectNumber(configFile.Global.TokenBody, projectNumber)
	if err != nil {
		return nil, fmt.Errorf("failed to replace project number in TokenBody: %v", err)
	}
	configFile.Global.TokenBody = newTokenBody

	configFile.Global.NetworkName = projectCRD.NetworkURL()
	configFile.Global.SubnetworkName = projectCRD.SubnetworkURL()

	return configFile, nil
}

// replaceTokenURLProjectNumber replaces the project number in the token URL.
// Original token URL is expected to be in the format:
// https://gkeauth.googleapis.com/v1/projects/{PROJECT_NUMBER}/locations/{LOCATION}/clusters/{CLUSTER_NAME}:generateToken
// This function replaces the {PROJECT_NUMBER} with the new project number while keeping the rest of the URL unchanged.
func replaceTokenURLProjectNumber(tokenURL string, projectNumber string) string {
	re := regexp.MustCompile(`(/projects/)([^/]*)(/)`)
	newTokenURL := re.ReplaceAllString(tokenURL, "${1}"+projectNumber+"${3}")
	return newTokenURL
}

// replaceTokenBodyProjectNumber replaces the project number in the token body.
// Original token body is expected to be in the format:
//
//		{
//		  "projectNumber": "1234567890"
//	   ... some other fields ...
//		}
//
// This function replaces the project number with the new project number while keeping the rest of the body unchanged.
func replaceTokenBodyProjectNumber(tokenBody string, projectNumber int64) (string, error) {
	var bodyMap map[string]interface{}

	// Unmarshal the JSON string into a map
	err := json.Unmarshal([]byte(tokenBody), &bodyMap)
	if err != nil {
		return "", fmt.Errorf("error unmarshaling TokenBody: %v", err)
	}

	// Replace the projectNumber with the new value
	bodyMap["projectNumber"] = projectNumber

	// Marshal the map back into a JSON string
	newTokenBodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return "", fmt.Errorf("error marshaling TokenBody: %v", err)
	}

	newTokenBody := string(newTokenBodyBytes)
	return newTokenBody, nil
}

// DefaultGCEConfigFile returns the default GCE config file.
// It reads config from the file and then parses it into a cloudgce.ConfigFile struct.
func DefaultGCEConfigFile(logger klog.Logger) *cloudgce.ConfigFile {
	if flags.F.ConfigFilePath == "" {
		return nil
	}

	logger.Info("Reading config from the specified path", "path", flags.F.ConfigFilePath)
	config, err := os.Open(flags.F.ConfigFilePath)
	if err != nil {
		klog.Fatalf("%v", err)
	}
	defer config.Close()

	allConfig, err := io.ReadAll(config)
	if err != nil {
		klog.Fatalf("Error while reading config (%q): %v", flags.F.ConfigFilePath, err)
	}
	logger.V(4).Info("Cloudprovider config file", "config", string(allConfig))

	cfg := &cloudgce.ConfigFile{}
	if err := gcfg.FatalOnly(gcfg.ReadInto(cfg, bytes.NewReader(allConfig))); err != nil {
		klog.Fatalf("Error while reading config (%q): %v", flags.F.ConfigFilePath, err)
	}

	return cfg
}

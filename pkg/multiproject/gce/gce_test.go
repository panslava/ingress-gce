package gce

import (
	"encoding/json"
	"testing"

	"github.com/google/go-cmp/cmp"
	cloudgce "k8s.io/cloud-provider-gcp/providers/gce"
	"k8s.io/ingress-gce/pkg/multiproject/projectcrd"
)

func TestReplaceTokenURLProjectNumber(t *testing.T) {
	testCases := []struct {
		name           string
		tokenURL       string
		projectNumber  string
		expectedResult string
	}{
		{
			name:           "Replace project number in URL",
			tokenURL:       "https://gkeauth.googleapis.com/v1/projects/12345/locations/us-central1/clusters/example-cluster:generateToken",
			projectNumber:  "654321",
			expectedResult: "https://gkeauth.googleapis.com/v1/projects/654321/locations/us-central1/clusters/example-cluster:generateToken",
		},
		{
			name:           "URL with non-numeric project number",
			tokenURL:       "https://gkeauth.googleapis.com/v1/projects/abcde/locations/us-central1/clusters/example-cluster:generateToken",
			projectNumber:  "654321",
			expectedResult: "https://gkeauth.googleapis.com/v1/projects/654321/locations/us-central1/clusters/example-cluster:generateToken",
		},
		{
			name:           "URL without project number",
			tokenURL:       "https://gkeauth.googleapis.com/v1/projects//locations/us-central1/clusters/example-cluster:generateToken",
			projectNumber:  "654321",
			expectedResult: "https://gkeauth.googleapis.com/v1/projects/654321/locations/us-central1/clusters/example-cluster:generateToken",
		},
		{
			name:           "Empty token URL",
			tokenURL:       "",
			projectNumber:  "654321",
			expectedResult: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := replaceTokenURLProjectNumber(tc.tokenURL, tc.projectNumber)
			if result != tc.expectedResult {
				t.Errorf("replaceTokenURLProjectNumber(%q, %q) = %q; want %q", tc.tokenURL, tc.projectNumber, result, tc.expectedResult)
			}
		})
	}
}

func TestReplaceTokenBodyProjectNumber(t *testing.T) {
	testCases := []struct {
		name           string
		tokenBody      string
		projectNumber  int64
		expectedResult string
		expectError    bool
	}{
		{
			name:           "Valid token body",
			tokenBody:      `{"projectNumber":12345,"clusterId":"example-cluster"}`,
			projectNumber:  654321,
			expectedResult: `{"clusterId":"example-cluster","projectNumber":654321}`,
			expectError:    false,
		},
		{
			name:           "Valid token body with extra fields",
			tokenBody:      `{"projectNumber":"oldNumber","clusterId":"example-cluster","isActive":true,"count":10}`,
			projectNumber:  654321,
			expectedResult: `{"clusterId":"example-cluster","count":10,"isActive":true,"projectNumber":654321}`,
			expectError:    false,
		},
		{
			name:           "Invalid JSON format",
			tokenBody:      `{"projectNumber":12345,"clusterId":"example-cluster"`,
			projectNumber:  654321,
			expectedResult: "",
			expectError:    true,
		},
		{
			name:           "Empty token body",
			tokenBody:      "",
			projectNumber:  654321,
			expectedResult: "",
			expectError:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := replaceTokenBodyProjectNumber(tc.tokenBody, tc.projectNumber)
			if (err != nil) != tc.expectError {
				t.Errorf("Expected error: %v, got: %v", tc.expectError, err)
				return
			}
			if !tc.expectError {
				// Normalize JSON strings for comparison
				expectedJSON := normalizeJSON(t, tc.expectedResult)
				resultJSON := normalizeJSON(t, result)

				if expectedJSON != resultJSON {
					t.Errorf("Expected result: %s, got: %s", expectedJSON, resultJSON)
				}
			}
		})
	}
}

// normalizeJSON helps to compare JSON strings without considering field order
func normalizeJSON(t *testing.T, jsonString string) string {
	var temp interface{}
	err := json.Unmarshal([]byte(jsonString), &temp)
	if err != nil {
		t.Fatalf("Invalid JSON string: %v", err)
	}
	normalizedBytes, err := json.Marshal(temp)
	if err != nil {
		t.Fatalf("Error marshaling JSON: %v", err)
	}
	return string(normalizedBytes)
}

func TestGenerateConfigFileForProject(t *testing.T) {
	testCases := []struct {
		name           string
		defaultConfig  cloudgce.ConfigFile
		projectCRD     *projectcrd.Project
		expectedConfig *cloudgce.ConfigFile
		expectError    bool
	}{
		{
			name: "Nil projectCRD returns default config",
			defaultConfig: cloudgce.ConfigFile{
				Global: cloudgce.ConfigGlobal{
					ProjectID:      "default-project-id",
					TokenURL:       "default-token-url",
					TokenBody:      "default-token-body",
					NetworkName:    "default-network",
					SubnetworkName: "default-subnetwork",
				},
			},
			projectCRD: nil,
			expectedConfig: &cloudgce.ConfigFile{
				Global: cloudgce.ConfigGlobal{
					ProjectID:      "default-project-id",
					TokenURL:       "default-token-url",
					TokenBody:      "default-token-body",
					NetworkName:    "default-network",
					SubnetworkName: "default-subnetwork",
				},
			},
			expectError: false,
		},
		{
			name: "Valid projectCRD replaces fields",
			defaultConfig: cloudgce.ConfigFile{
				Global: cloudgce.ConfigGlobal{
					ProjectID:      "default-project-id",
					TokenURL:       "https://gkeauth.googleapis.com/v1/projects/12345/locations/us-central1/clusters/example-cluster:generateToken",
					TokenBody:      `{"projectNumber":12345,"clusterId":"example-cluster"}`,
					NetworkName:    "default-network",
					SubnetworkName: "default-subnetwork",
				},
			},
			projectCRD: &projectcrd.Project{
				Spec: projectcrd.ProjectSpec{
					ProjectID:     "project-crd-project-id",
					ProjectNumber: 654321,
					NetworkConfig: projectcrd.NetworkConfig{
						Network:           "project-crd-network-url",
						DefaultSubnetwork: "project-crd-subnetwork-url",
					},
				},
			},
			expectedConfig: &cloudgce.ConfigFile{
				Global: cloudgce.ConfigGlobal{
					ProjectID:      "project-crd-project-id",
					TokenURL:       "https://gkeauth.googleapis.com/v1/projects/654321/locations/us-central1/clusters/example-cluster:generateToken",
					TokenBody:      `{"clusterId":"example-cluster","projectNumber":654321}`,
					NetworkName:    "project-crd-network-url",
					SubnetworkName: "project-crd-subnetwork-url",
				},
			},
			expectError: false,
		},
		{
			name: "Error replacing TokenBody",
			defaultConfig: cloudgce.ConfigFile{
				Global: cloudgce.ConfigGlobal{
					TokenBody: `{"projectNumber":12345,"clusterId":"example-cluster"`, // Invalid JSON
				},
			},
			projectCRD: &projectcrd.Project{
				Spec: projectcrd.ProjectSpec{
					ProjectID:     "project-crd-project-id",
					ProjectNumber: 654321,
				},
			},
			expectedConfig: nil,
			expectError:    true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			config, err := generateConfigFileForProject(tc.defaultConfig, tc.projectCRD)
			if (err != nil) != tc.expectError {
				t.Errorf("Expected error: %v, got: %v", tc.expectError, err)
			}
			if !tc.expectError {
				if diff := cmp.Diff(config, tc.expectedConfig); diff != "" {
					t.Errorf("generateConfigFileForProject() mismatch (-want +got):\n%s", diff)
				}
			}
		})
	}
}

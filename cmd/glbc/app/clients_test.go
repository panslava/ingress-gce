/*
Copyright 2017 The Kubernetes Authors.

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

package app

import (
	"bytes"
	"io"
	"testing"
)

// TestGenerateConfigReader tests the generated reader func returns the same
// content on retry.
func TestGenerateConfigReaderFunc(t *testing.T) {
	expectedConfig := []byte{'g', 'l', 'b', 'c'}
	configReaderFunc := generateConfigReaderFunc(expectedConfig)
	for i := 0; i < 10; i++ {
		config, err := io.ReadAll(configReaderFunc())
		if err != nil {
			t.Fatalf("Error while reading config: %v", err)
		}
		if !bytes.Equal(expectedConfig, config) {
			t.Fatalf("Unexpected config, want: %v, got %v", string(expectedConfig), string(config))
		}
	}
}

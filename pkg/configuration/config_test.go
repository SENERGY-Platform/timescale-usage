/*
 *    Copyright (c) 2026 InfAI (CC SES)
 *
 *    Licensed under the Apache License, Version 2.0 (the "License");
 *    you may not use this file except in compliance with the License.
 *    You may obtain a copy of the License at
 *
 *        http://www.apache.org/licenses/LICENSE-2.0
 *
 *    Unless required by applicable law or agreed to in writing, software
 *    distributed under the License is distributed on an "AS IS" BASIS,
 *    WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 *    See the License for the specific language governing permissions and
 *    limitations under the License.
 */

package configuration

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestHandleEnvironmentVarsMasksSecrets(t *testing.T) {
	t.Setenv("POSTGRES_PW", "the-password")
	t.Setenv("POSTGRES_HOST", "the-host")
	config := &ConfigStruct{}

	stdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	HandleEnvironmentVars(config)
	os.Stdout = stdout
	w.Close()
	out, _ := io.ReadAll(r)

	if strings.Contains(string(out), "the-password") {
		t.Errorf("password logged: %q", out)
	}
	if !strings.Contains(string(out), "the-host") {
		t.Errorf("other values not logged: %q", out)
	}
	if config.PostgresPw != "the-password" {
		t.Errorf("password not applied: %q", config.PostgresPw)
	}
}

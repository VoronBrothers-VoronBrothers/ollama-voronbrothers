package envconfig

import (
	"strings"
	"testing"
)

// extraDocumentedVars are read directly via os.Getenv / Var in the codebase
// but are not part of AsMap(), so the template test checks them too.
var extraDocumentedVars = []string{
	"OLLAMA_AGENT_DISABLE_SHELL",
	"OLLAMA_AGENT_DISABLE_WEBSEARCH",
	"OLLAMA_APP_DB_PATH",
	"OLLAMA_AUTH",
	"OLLAMA_CLOUD_BASE_URL",
	"OLLAMA_CORS",
	"OLLAMA_DOT_COM_URL",
	"OLLAMA_EXPERIMENT",
	"OLLAMA_EXTERNAL_SYSTEM_PROMPT",
	"OLLAMA_HOME",
	"OLLAMA_PROVIDERS_CONFIG",
	"OLLAMA_SKILLS",
	"LAMA_MODEL",
}

// TestEnvFileTemplateDocumentsAllVars guards against the reference template in
// ~/.ollama/env drifting behind the code: every variable known to the binary
// must appear (at least commented out) in envFileTemplate.
func TestEnvFileTemplateDocumentsAllVars(t *testing.T) {
	for key := range AsMap() {
		if !strings.Contains(envFileTemplate, key+"=") {
			t.Errorf("envFileTemplate does not document %s (present in AsMap)", key)
		}
	}
	for _, key := range extraDocumentedVars {
		if !strings.Contains(envFileTemplate, key+"=") {
			t.Errorf("envFileTemplate does not document %s", key)
		}
	}
}

// TestEnvFileTemplateIsCommentedOut ensures the recreated user file stays a
// pure reference: it must not silently override /etc/ollama/env.
func TestEnvFileTemplateIsCommentedOut(t *testing.T) {
	for i, line := range strings.Split(envFileTemplate, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		t.Errorf("line %d is active (would override /etc/ollama/env): %q", i+1, trimmed)
	}
}

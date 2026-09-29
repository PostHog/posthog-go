package posthogmcp

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Fixtures from posthog-python's test_code_variables.py. Vendor keys are
// assembled from prefix + body so no complete secret literal lives in source.
func TestLooksLikeSecret(t *testing.T) {
	for _, test := range []struct {
		value string
		want  bool
	}{
		{"sk-proj-" + "T3BlbkFJabcd1234efgh5678ijkl9012mnop3456qrst7890wxyz", true},            // OpenAI
		{"sk-" + "Hf8sJd72hsKbNd83jdH5sQp2T3BlbkFJabcdEFGH1234ijklMNOPqrst", true},             // OpenAI legacy
		{"sk-ant-" + "api03-aBcDeFgHiJkLmNoPqRsTuVwX0123456789-AbCdEf_gHiJkLmQQ", true},        // Anthropic
		{"AKIA" + "IOSFODNN7EXAMPLE", true},                                                    // AWS access key id (AWS's own doc example)
		{"sk_live_" + "4eC39HqLyjWDarjtT1zdp7dc", true},                                        // Stripe secret key
		{"pk_live_" + "TYooMQauvdEDq54NiTphI7jx", true},                                        // Stripe publishable key
		{"ghp_" + "16C7e42F292c6912E7710c838347Ae178B4a", true},                                // GitHub PAT
		{"github_pat_" + "11ABCDEFG0aBcDeFgHiJkL_mNoPqRsTuVwXyZ0123456789abcdef", true},        // GitHub
		{"glpat-" + "aB1cD2eF3gH4iJ5kL6mN", true},                                              // GitLab PAT
		{"xoxb-" + "1234567890-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx", true},                  // Slack bot token
		{"AIza" + "SyD-1a2B3c4D5e6F7g8H9i0JkLmNoPqRsTuVw", true},                               // Google API key
		{"eyJ" + "hbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N", true}, // JWT
		{"hf_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ01234567", true},                                   // Hugging Face
		{"ya29." + "a0AfH6SMBx1y2z3-_abcDEFghiJKLmnoPQ", true},                                 // Google OAuth
		{"sq0atp-" + "1a2B3c4D5e6F7g8H9i0JkL", true},                                           // Square
		{"glsa_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ012345_a1b2c3d4", true},                          // Grafana
		{"SK" + "0123456789abcdef0123456789abcdef", true},                                      // Twilio
		{"SG." + "aBcDeFgHiJkLmNoPqRsTuV.abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ", true},   // SendGrid
		{"npm_" + "aBcDeFgHiJkLmNoPqRsTuVwXyZ0123456789", true},                                // npm
		{"dapi" + "0123456789abcdef0123456789abcdef", true},                                    // Databricks
		{"PMAK-" + "0123456789abcdef01234567-0123456789abcdef0123456789abcdef0123", true},      // Postman
		{"lin_api_" + "0123456789abcdef0123456789abcdef01234567", true},                        // Linear
		{"shpat_" + "0123456789abcdef0123456789abcdef", true},                                  // Shopify
		{"NRAK-" + "0123456789ABCDEFGHIJKLMNOPQ", true},                                        // New Relic
		{"0123456789abcdef0123456789abcdef" + "-us12", true},                                   // Mailchimp
		{"-----BEGIN RSA PRIVATE KEY-----\nMIIEpAIBAAKCAQEA1234", true},                        // PEM private key
		{"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXkt", true},                        // OpenSSH private key
		{"wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", true},                                     // AWS secret key (no prefix, base64)
		{"xK9#mP2$vL5nQ8w!", true},                                                             // strong password with symbols
		{"P@ssw0rd!2024#Secure$Key", true},                                                     // strong password
		{"xK9mP2vL5nQ8wRtZ", true},                                                             // strong password, no symbols
		{"n8fK2pQ9vX7mL4wR8tY3uZ6bC1dE5gH", true},                                              // random mixed-case+digit token
		{"dGhpc2lzYVNlY3JldFRva2VuMTIzNA==", true},                                             // base64 blob
		{"550e8400-e29b-41d4-a716-446655440000", false},                                        // UUID v4 (lowercase)
		{"F47AC10B-58CC-4372-A567-0E02B2C3D479", false},                                        // UUID (uppercase)
		{"507f1f77bcf86cd799439011", false},                                                    // Mongo ObjectId
		{"e83c5163316f89bfbde7d9ab23ca2e25604af290", false},                                    // git SHA-1
		{"d41d8cd98f00b204e9800998ecf8427e", false},                                            // md5 hex digest
		{"9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", false},            // sha256 hex
		{"a7F2c9E1b4D8a3C6e0F5b2D9c7A1e4F8", false},                                            // pure mixed-case hex (treated as an id)
		{"CheckActivityInput(proxy_record_id=UUID('019df333-e9e2-0000-fa8e-ba3dd4217c09'))", false},
		{"<posthog.temporal.common._ActivityInterceptor object at 0xffff77a7a850>", false},
		{"ExecuteActivityInput(fn=check_status,args=[Input(id=42,name=widget)])", false},
		{"user_authentication_handler", false},                               // snake_case identifier
		{"getUserByIdAndOrganization", false},                                // camelCase identifier
		{"getUserById2024", false},                                           // camelCase with digits
		{"ApplicationConfigurationManager", false},                           // PascalCase class name
		{"PENDING_APPROVAL", false},                                          // SCREAMING_CASE enum
		{"created-at-descending", false},                                     // dashed slug
		{"application/json", false},                                          // mime type
		{"alice.smith@example.com", false},                                   // email
		{"the quick brown fox jumps over", false},                            // prose (has spaces)
		{"/usr/local/lib/python3.13/site-packages/posthog/client.py", false}, // unix path
		{"C:\\Users\\admin\\app\\config.yaml", false},                        // windows path
		{"https://api.example.com/v2/users/12345/orders", false},             // url
		{"1234567890123456", false},                                          // long number
		{"3.141592653589793", false},                                         // float
		{"2026-06-23T11:11:00.000Z", false},                                  // ISO timestamp
		{"v1.2.3-beta.4", false},                                             // version string
		{"active", false},                                                    // short word
		{"xK9#mP2$", false},                                                  // short strings bail before the entropy pass
	} {
		assert.Equal(t, test.want, looksLikeSecret(test.value), test.value)
	}
}

package serve

import "testing"

func TestEnvBoolPreservesParseBoolForms(t *testing.T) {
	for _, value := range []string{"1", "t", "T", "true", "TRUE", "True"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("BANHMI_TEST_BOOL", value)
			if !envBool("BANHMI_TEST_BOOL") {
				t.Fatalf("envBool rejected %q", value)
			}
		})
	}
	for _, value := range []string{"", "0", "f", "F", "false", "yes", "invalid"} {
		t.Run("false_"+value, func(t *testing.T) {
			t.Setenv("BANHMI_TEST_BOOL", value)
			if envBool("BANHMI_TEST_BOOL") {
				t.Fatalf("envBool accepted %q", value)
			}
		})
	}
}

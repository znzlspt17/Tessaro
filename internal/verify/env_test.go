package verify

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestProcessEnvironmentsDoNotInheritMarker(t *testing.T) {
	t.Setenv("JEVCANCODING_SECRET_MARKER", "fake-marker")
	for _, env := range [][]string{compileEnv(t.TempDir()), executeEnv(t.TempDir())} {
		for _, v := range env {
			if strings.HasPrefix(v, "JEVCANCODING_SECRET_MARKER=") {
				t.Fatal("marker inherited")
			}
		}
		exe, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(exe, "-test.run=TestEnvironmentChild")
		cmd.Env = append(env, "JEVCANCODING_TEST_CHILD=1")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("child: %s %v", b, err)
		}
	}
}
func TestEnvironmentChild(t *testing.T) {
	if os.Getenv("JEVCANCODING_TEST_CHILD") != "1" {
		return
	}
	if _, ok := os.LookupEnv("JEVCANCODING_SECRET_MARKER"); ok {
		t.Fatal("marker visible to child")
	}
}

package hostdatabase

import (
	"strings"
	"testing"

	"provision/internal/config"
	"provision/internal/host"
)

func TestImageRegistry(t *testing.T) {
	def, ok := ImageFor("")
	if !ok || def.Manifest != ImageManifest || def.Generation() != "postgresql-17-6-b86568d3e0fe" {
		t.Fatalf("default image = %+v, %v", def, ok)
	}
	second, ok := ImageFor("17.7")
	if !ok || second.Generation() != "postgresql-17-7-030da09481c3" || second.Reference() != "docker.io/library/postgres@"+second.Manifest {
		t.Fatalf("second image = %+v, %v", second, ok)
	}
	if _, ok := ImageFor("17.99"); ok {
		t.Fatal("unregistered version resolved")
	}
	if !CompatibleImages(def.Manifest, second.Manifest) || CompatibleImages(def.Manifest, "sha256:"+strings.Repeat("b", 64)) {
		t.Fatal("same-major compatibility is wrong")
	}
}

func TestTargetRetargetsCapabilityToDeclaredVersion(t *testing.T) {
	capability := host.DatabaseCapabilities{
		PostgreSQLVersion: PostgreSQLVersion, PostgreSQLImageIndex: ImageIndex, PostgreSQLImageManifest: ImageManifest,
		PostgreSQLGeneration:         "postgresql-17-6-b86568d3e0fe",
		PostgreSQLGenerationDataPath: "/var/lib/provision/environments/lab/services/postgresql/generations/postgresql-17-6-b86568d3e0fe/data",
	}
	compiled := databaseCompiled(t, "17.7")
	got := Target(compiled, capability)
	if got.PostgreSQLVersion != "17.7" || got.PostgreSQLGeneration != "postgresql-17-7-030da09481c3" ||
		!strings.Contains(got.PostgreSQLGenerationDataPath, "/generations/postgresql-17-7-030da09481c3/data") {
		t.Fatalf("retargeted capability = %+v", got)
	}
	if same := Target(databaseCompiled(t, ""), capability); same != capability {
		t.Fatalf("default version changed the capability: %+v", same)
	}
}

func databaseCompiled(t *testing.T, version string) config.Compiled {
	t.Helper()
	return config.Compiled{
		Application: config.Application{Components: map[string]config.Component{"data": {Role: "database"}}},
		Environment: config.Environment{Implementations: map[string]config.Implementation{"data": {Version: version}}},
	}
}

package servicecatalog

import "testing"

func TestDirectoryValidatesContentsBeforeAcceptingVersion(t *testing.T) {
	catalog, err := LoadEmbedded()
	if err != nil {
		t.Fatal(err)
	}
	compiled, err := catalog.Compile(productionTopology(), "control")
	if err != nil {
		t.Fatal(err)
	}
	original := compiled.Directory
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	hash, err := original.VersionHash()
	if err != nil || original.Version != hash {
		t.Fatalf("compiler and consumer version mismatch: %v", err)
	}
	for name, change := range map[string]func(*Directory){
		"unknown host":   func(d *Directory) { d.Services["trpc.moox.test.Unknown"] = []string{"missing"} },
		"duplicate host": func(d *Directory) { d.Services["trpc.moox.test.Unknown"] = []string{"control", "control"} },
		"bad service":    func(d *Directory) { d.Services["/trpc.moox.test.Unknown/"] = []string{"control"} },
		"bad address":    func(d *Directory) { d.Hosts["control"] = DirectoryHost{Address: "host.example:11003"} },
		"nil map":        func(d *Directory) { d.Services = nil },
	} {
		t.Run(name, func(t *testing.T) {
			d := original.Clone()
			change(&d)
			d.Version, err = d.VersionHash()
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Validate(); err == nil {
				t.Fatal("a matching hash cannot make invalid contents acceptable")
			}
		})
	}
	d := original.Clone()
	d.Hosts["control"] = DirectoryHost{Address: "changed.example.test"}
	if err := d.Validate(); err == nil {
		t.Fatal("directory contents changed without updating the version")
	}
	if err := original.Validate(); err != nil {
		t.Fatalf("clone changed the original: %v", err)
	}
	if clone := (Directory{}).Clone(); clone.Services != nil || clone.Hosts != nil {
		t.Fatal("empty clone changed nil maps")
	}
}

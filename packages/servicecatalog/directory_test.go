package servicecatalog

import "testing"

func TestDirectoryValidatesContents(t *testing.T) {
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
	// 版本由下发方给出，只用来判断有没有变化：Validate 不核对它。
	versioned := original.Clone()
	versioned.Version = "any-version"
	if err := versioned.Validate(); err != nil {
		t.Fatalf("directory version must not affect validation: %v", err)
	}
	for name, change := range map[string]func(*Directory){
		"unknown host":   func(d *Directory) { d.Services["trpc.moox.test.Unknown"] = []string{"missing"} },
		"duplicate host": func(d *Directory) { d.Services["trpc.moox.test.Unknown"] = []string{"control", "control"} },
		"bad service":    func(d *Directory) { d.Services["/trpc.moox.test.Unknown/"] = []string{"control"} },
		"bad address":    func(d *Directory) { d.Hosts["control"] = DirectoryHost{Address: "host.example:11003"} },
		"nil map":        func(d *Directory) { d.Services = nil },
		"missing relay":  func(d *Directory) { d.Hosts["storage"] = DirectoryHost{Address: "146.56.196.204", Relay: "missing"} },
		"self relay":     func(d *Directory) { d.Hosts["storage"] = DirectoryHost{Address: "146.56.196.204", Relay: "storage"} },
		"chained relay": func(d *Directory) {
			d.Hosts["storage"] = DirectoryHost{Address: "146.56.196.204", Relay: "overseas"}
			d.Hosts["overseas"] = DirectoryHost{Address: "43.132.204.177", Relay: "control"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := original.Clone()
			change(&d)
			if err := d.Validate(); err == nil {
				t.Fatal("invalid directory contents accepted")
			}
		})
	}
	d := original.Clone()
	d.Hosts["control"] = DirectoryHost{Address: "changed.example.test"}
	d.Services["trpc.moox.storage.PrimaryStore"][0] = "control"
	if original.Hosts["control"].Address == "changed.example.test" || original.Services["trpc.moox.storage.PrimaryStore"][0] != "storage" {
		t.Fatal("clone shares maps or slices with the original")
	}
	if err := original.Validate(); err != nil {
		t.Fatalf("clone changed the original: %v", err)
	}
	if clone := (Directory{}).Clone(); clone.Services != nil || clone.Hosts != nil {
		t.Fatal("empty clone changed nil maps")
	}
}

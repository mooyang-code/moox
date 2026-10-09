package doctor

import (
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/packages/servicecatalog"
)

// MaxManifestComponents 是诊断清单最多包含的组件数。
const MaxManifestComponents = 64

// Manifest 是诊断用的组件清单，由组件目录生成。
type Manifest struct {
	Components []Component `json:"components"`
	// Checksum 是组件目录的校验和，诊断报告据此标明依据的是哪一版目录。
	Checksum string `json:"checksum"`
}

// Component 是组件目录中的一种可部署组件。
type Component struct {
	ComponentID string                    `json:"component_id"`
	Name        string                    `json:"name"`
	Transport   servicecatalog.Transport  `json:"transport"`
	Functional  servicecatalog.Functional `json:"functional_observability"`
	// HealthKind 是探测方式；HealthPath 只在 readyz 方式下有值。
	HealthKind servicecatalog.HealthKind `json:"health_kind"`
	HealthPath string                    `json:"health_path,omitempty"`
	HealthPort int                       `json:"health_port,omitempty"`
}

// LoadEmbeddedManifest 由内置的组件目录生成诊断清单。
func LoadEmbeddedManifest() (Manifest, error) {
	return ManifestFromCatalog(servicecatalog.Default())
}

// ManifestFromCatalog 由组件目录生成诊断清单，组件顺序与目录一致。
func ManifestFromCatalog(catalog *servicecatalog.Catalog) (Manifest, error) {
	if catalog == nil {
		return Manifest{}, errors.New("组件目录为空")
	}
	if len(catalog.Components) > MaxManifestComponents {
		return Manifest{}, fmt.Errorf("组件目录有 %d 个组件，诊断清单最多 %d 个", len(catalog.Components), MaxManifestComponents)
	}
	manifest := Manifest{Checksum: catalog.Checksum(), Components: make([]Component, 0, len(catalog.Components))}
	for _, component := range catalog.Components {
		item := Component{
			ComponentID: component.ID, Name: component.Name,
			Transport: component.Observability.Transport, Functional: component.Observability.Functional,
			HealthKind: component.Health.Kind, HealthPort: component.Health.Port,
		}
		if component.Health.Kind == servicecatalog.HealthReadyz {
			item.HealthPath = "/readyz"
		}
		manifest.Components = append(manifest.Components, item)
	}
	return manifest, nil
}

// Component 按组件 ID 查找组件。
func (m Manifest) Component(componentID string) (Component, bool) {
	for _, component := range m.Components {
		if component.ComponentID == componentID {
			return component, true
		}
	}
	return Component{}, false
}

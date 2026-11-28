// @vitest-environment node
import { describe, expect, it } from "vitest";
import { extractSpacePolicies, generateSpacePolicyModuleFromYAML, resolveSpacePolicy } from "../build/space-policy-plugin";

describe("space-policy-plugin", () => {
  it("支持服务级 space_optional，且方法级策略优先于服务级（与后端 ConsoleSpacePolicy 一致）", () => {
    const yaml = `
components:
  - services:
      - path: trpc.moox.demo.OptionalSvc
        console_name: optional-svc
        space_policy: space_optional
        methods:
          - name: DefaultMethod
          - name: GlobalOverride
            space_policy: global
          - name: RequiredOverride
            space_policy: space_required
      - path: trpc.moox.demo.GlobalSvc
        console_name: global-svc
        space_policy: global
        methods:
          - name: DefaultMethod
          - name: OptionalOverride
            space_policy: space_optional
          - name: RequiredOverride
            space_policy: space_required
      - path: trpc.moox.demo.DefaultSvc
        console_name: default-svc
        methods:
          - name: DefaultMethod
          - name: OptionalOverride
            space_policy: space_optional
          - name: GlobalOverride
            space_policy: global
`;

    const table = extractSpacePolicies(yaml, "test-catalog.yaml");

    expect(resolveSpacePolicy(table, "optional-svc", "DefaultMethod")).toBe("space_optional");
    expect(resolveSpacePolicy(table, "optional-svc", "GlobalOverride")).toBe("global");
    expect(resolveSpacePolicy(table, "optional-svc", "RequiredOverride")).toBe("space_required");

    expect(resolveSpacePolicy(table, "global-svc", "DefaultMethod")).toBe("global");
    expect(resolveSpacePolicy(table, "global-svc", "OptionalOverride")).toBe("space_optional");
    expect(resolveSpacePolicy(table, "global-svc", "RequiredOverride")).toBe("space_required");

    expect(resolveSpacePolicy(table, "default-svc", "DefaultMethod")).toBe("space_required");
    expect(resolveSpacePolicy(table, "default-svc", "OptionalOverride")).toBe("space_optional");
    expect(resolveSpacePolicy(table, "default-svc", "GlobalOverride")).toBe("global");
    expect(resolveSpacePolicy(table, "unknown-svc", "AnyMethod")).toBe("space_required");

    const generated = generateSpacePolicyModuleFromYAML(yaml, "test-catalog.yaml");
    expect(generated).toContain('"optional-svc": "space_optional"');
    expect(generated).toContain('"RequiredOverride": "space_required"');
  });

  it("拒绝非法 space_policy 或无 console_name 的服务级 space_policy", () => {
    expect(() =>
      extractSpacePolicies(
        `
components:
  - services:
      - path: trpc.moox.demo.Bad
        console_name: bad
        space_policy: invalid_tier
`,
        "bad.yaml"
      )
    ).toThrow(/非法/);

    expect(() =>
      extractSpacePolicies(
        `
components:
  - services:
      - path: trpc.moox.demo.Internal
        space_policy: space_optional
`,
        "internal.yaml"
      )
    ).toThrow(/console_name/);
  });
});

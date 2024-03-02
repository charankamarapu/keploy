package config

import (
	yaml3 "gopkg.in/yaml.v3"
	"sigs.k8s.io/kustomize/kyaml/yaml"
	"sigs.k8s.io/kustomize/kyaml/yaml/merge2"
	"sigs.k8s.io/kustomize/kyaml/yaml/walk"
)

const DefaultConfig = `
path: ""
command: ""
port: 0
proxyPort: 16789
dnsPort: 26789
debug: false
telemetry: false
inDocker: false
containerName: ""
networkName: ""
buildDelay: 30s
test:
  selectedTests: {}
  globalNoise:
    global: {}
    test-sets: {}
  delay: 10
  apiTimeout: 5
  coverage: false
  coverageReportPath: ""
  ignoreOrdering: true
  mongoPassword: "default@123"
  language: ""
record:
  filters: []
configPath: ""
bypassRules: []
`

const InternalConfig = `
keployContainer: "keploy-v2"
keployNetwork: "keploy-network"
`

var config = &Config{}

func New() *Config {
	// merge default config with internal config
	mergedConfig, err := Merge(DefaultConfig, InternalConfig)
	if err != nil {
		panic(err)

	}
	err = yaml3.Unmarshal([]byte(mergedConfig), config)
	if err != nil {
		panic(err)
	}
	return config
}

func Merge(srcStr, destStr string) (string, error) {
	return mergeStrings(srcStr, destStr, false, yaml.MergeOptions{})
}

// Reference: https://github.com/kubernetes-sigs/kustomize/blob/537c4fa5c2bf3292b273876f50c62ce1c81714d7/kyaml/yaml/merge2/merge2.go#L24
// VisitKeysAsScalars is set to true to enable merging comments.
// inferAssociativeLists is set to fasle to disable merging associative lists.
func mergeStrings(srcStr, destStr string, infer bool, mergeOptions yaml.MergeOptions) (string, error) {
	src, err := yaml.Parse(srcStr)
	if err != nil {
		return "", err
	}

	dest, err := yaml.Parse(destStr)
	if err != nil {
		return "", err
	}

	result, err := walk.Walker{
		Sources:               []*yaml.RNode{dest, src},
		Visitor:               merge2.Merger{},
		InferAssociativeLists: infer,
		VisitKeysAsScalars:    true,
		MergeOptions:          mergeOptions,
	}.Walk()
	if err != nil {
		return "", err
	}

	return result.String()
}

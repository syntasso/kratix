//go:build !extension

package config

type kratixPipelineConfig struct{}

func NewPipelineConfig() *kratixPipelineConfig {
	return &kratixPipelineConfig{}
}

func (c *kratixPipelineConfig) PipelineNamePrefix() string {
	return "kratix"
}

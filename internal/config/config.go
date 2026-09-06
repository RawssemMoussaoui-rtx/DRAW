package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"draw/internal/model"
)

type ScoringWeights struct {
	Priority           float64
	InformationValue   float64
	FreshnessValue     float64
	SourceValue        float64
	ContradictionValue float64
	ContradictionAlpha  float64
	ContradictionScale  float64
	Cost               float64
	ErrorRisk          float64
}

type RetryPolicy struct {
	MaxAttempts int
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

type UpgradePolicy struct {
	BrowserMaxConcurrency int
}

type ReplanTriggers struct {
	KContradictions int
	MMissingPrimary int
	SStaleSources   int
}

type SchedulerConfig struct {
	MaxGlobalConcurrency int
	DomainLimits          map[string]int
	CPUThreshold          float64
	RAMThreshold          float64
	HysteresisWindow      time.Duration
	Cooldown              time.Duration
	CostModel             map[model.TaskType]int
	ScoringWeights        ScoringWeights
	ExplorationQuotaFloor float64
	Retry                 RetryPolicy
	Upgrade               UpgradePolicy
	Replan                ReplanTriggers
	SessionRetention      time.Duration
}

const (
	EnvConfigPath            = "DRAW_CONFIG"
	EnvMaxGlobalConcurrency  = "RD_SCHED_MAX_CONCURRENCY"
	EnvCPUThreshold          = "RD_SCHED_CPU_THRESHOLD"
	EnvRAMThreshold          = "RD_SCHED_RAM_THRESHOLD"
	EnvSessionRetention      = "RD_SCHED_SESSION_RETENTION"
	EnvBrowserMaxConcurrency = "RD_SCHED_BROWSER_MAX"
)

type retryFile struct {
	MaxAttempts int    `toml:"MaxAttempts"`
	BackoffBase string `toml:"BackoffBase"`
	BackoffMax  string `toml:"BackoffMax"`
}

type fileConfig struct {
	MaxGlobalConcurrency int                `toml:"MaxGlobalConcurrency"`
	DomainLimits          map[string]int     `toml:"DomainLimits"`
	CPUThreshold          float64            `toml:"CPUThreshold"`
	RAMThreshold          float64            `toml:"RAMThreshold"`
	HysteresisWindow      string             `toml:"HysteresisWindow"`
	Cooldown              string             `toml:"Cooldown"`
	CostModel             map[string]int     `toml:"CostModel"`
	ScoringWeights        ScoringWeights     `toml:"ScoringWeights"`
	ExplorationQuotaFloor float64             `toml:"ExplorationQuotaFloor"`
	Retry                 retryFile          `toml:"Retry"`
	Upgrade               UpgradePolicy      `toml:"Upgrade"`
	Replan                ReplanTriggers     `toml:"Replan"`
	SessionRetention      string             `toml:"SessionRetention"`
}

func DefaultCostModel() map[model.TaskType]int {
	return model.DefaultCostModel()
}

func DefaultScoringWeights() ScoringWeights {
	return ScoringWeights{
		Priority:           1.0,
		InformationValue:   1.0,
		FreshnessValue:     0.5,
		SourceValue:        0.8,
		ContradictionValue: 1.5,
		ContradictionAlpha: 2.0,
		ContradictionScale: 1.0,
		Cost:               1.0,
		ErrorRisk:          0.5,
	}
}

func DefaultRetry() RetryPolicy {
	return RetryPolicy{MaxAttempts: 3, BackoffBase: time.Second, BackoffMax: time.Minute}
}

func DefaultUpgrade() UpgradePolicy {
	return UpgradePolicy{BrowserMaxConcurrency: 4}
}

func DefaultReplan() ReplanTriggers {
	return ReplanTriggers{KContradictions: 2, MMissingPrimary: 2, SStaleSources: 2}
}

func Defaults() SchedulerConfig {
	return SchedulerConfig{
		MaxGlobalConcurrency: 10,
		DomainLimits:          map[string]int{},
		CPUThreshold:          0.8,
		RAMThreshold:          0.8,
		HysteresisWindow:      10 * time.Second,
		Cooldown:              5 * time.Second,
		CostModel:             DefaultCostModel(),
		ScoringWeights:        DefaultScoringWeights(),
		ExplorationQuotaFloor:   0.20,
		Retry:                 DefaultRetry(),
		Upgrade:               DefaultUpgrade(),
		Replan:                DefaultReplan(),
		SessionRetention:      24 * time.Hour,
	}
}

func Load(path string) (SchedulerConfig, error) {
	cfg := Defaults()
	if strings.TrimSpace(path) != "" {
		var fc fileConfig
		if _, err := toml.DecodeFile(path, &fc); err != nil {
			return SchedulerConfig{}, err
		}
		if err := merge(&cfg, &fc); err != nil {
			return SchedulerConfig{}, err
		}
	}
	cfg = cfg.applyEnv()
	if err := cfg.Validate(); err != nil {
		return SchedulerConfig{}, err
	}
	return cfg, nil
}

func merge(dst *SchedulerConfig, src *fileConfig) error {
	if src.MaxGlobalConcurrency > 0 {
		dst.MaxGlobalConcurrency = src.MaxGlobalConcurrency
	}
	for dom, lim := range src.DomainLimits {
		if dst.DomainLimits == nil {
			dst.DomainLimits = map[string]int{}
		}
		dst.DomainLimits[dom] = lim
	}
	if src.CPUThreshold != 0 {
		dst.CPUThreshold = src.CPUThreshold
	}
	if src.RAMThreshold != 0 {
		dst.RAMThreshold = src.RAMThreshold
	}
	if src.HysteresisWindow != "" {
		if d, err := time.ParseDuration(src.HysteresisWindow); err == nil {
			dst.HysteresisWindow = d
		}
	}
	if src.Cooldown != "" {
		if d, err := time.ParseDuration(src.Cooldown); err == nil {
			dst.Cooldown = d
		}
	}
	if src.CostModel != nil {
		m := map[model.TaskType]int{}
		for k, v := range src.CostModel {
			tt, ok := model.TaskTypes[k]
			if !ok {
				return fmt.Errorf("config: unknown TaskType cost key %q", k)
			}
			m[tt] = v
		}
		for k, v := range model.DefaultCostModel() {
			if _, exists := m[k]; !exists {
				m[k] = v
			}
		}
		dst.CostModel = m
	}
	if src.Retry.MaxAttempts > 0 {
		dst.Retry.MaxAttempts = src.Retry.MaxAttempts
	}
	if src.Retry.BackoffBase != "" {
		if d, err := time.ParseDuration(src.Retry.BackoffBase); err == nil {
			dst.Retry.BackoffBase = d
		}
	}
	if src.Retry.BackoffMax != "" {
		if d, err := time.ParseDuration(src.Retry.BackoffMax); err == nil {
			dst.Retry.BackoffMax = d
		}
	}
	if src.Upgrade.BrowserMaxConcurrency > 0 {
		dst.Upgrade.BrowserMaxConcurrency = src.Upgrade.BrowserMaxConcurrency
	}
	sw := src.ScoringWeights
	if sw.Priority != 0 {
		dst.ScoringWeights.Priority = sw.Priority
	}
	if sw.InformationValue != 0 {
		dst.ScoringWeights.InformationValue = sw.InformationValue
	}
	if sw.FreshnessValue != 0 {
		dst.ScoringWeights.FreshnessValue = sw.FreshnessValue
	}
	if sw.SourceValue != 0 {
		dst.ScoringWeights.SourceValue = sw.SourceValue
	}
	if sw.ContradictionValue != 0 {
		dst.ScoringWeights.ContradictionValue = sw.ContradictionValue
	}
	if sw.Cost != 0 {
		dst.ScoringWeights.Cost = sw.Cost
	}
	if sw.ErrorRisk != 0 {
		dst.ScoringWeights.ErrorRisk = sw.ErrorRisk
	}
	if sw.ContradictionAlpha != 0 {
		dst.ScoringWeights.ContradictionAlpha = sw.ContradictionAlpha
	}
	if sw.ContradictionScale != 0 {
		dst.ScoringWeights.ContradictionScale = sw.ContradictionScale
	}
	if src.ExplorationQuotaFloor > 0 {
		dst.ExplorationQuotaFloor = src.ExplorationQuotaFloor
	}
	if src.Replan.KContradictions > 0 {
		dst.Replan.KContradictions = src.Replan.KContradictions
	}
	if src.Replan.MMissingPrimary > 0 {
		dst.Replan.MMissingPrimary = src.Replan.MMissingPrimary
	}
	if src.Replan.SStaleSources > 0 {
		dst.Replan.SStaleSources = src.Replan.SStaleSources
	}
	if src.SessionRetention != "" {
		if d, err := time.ParseDuration(src.SessionRetention); err == nil {
			dst.SessionRetention = d
		}
	}
	return nil
}

func (c SchedulerConfig) applyEnv() SchedulerConfig {
	if v := os.Getenv(EnvMaxGlobalConcurrency); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.MaxGlobalConcurrency = n
		}
	}
	if v := os.Getenv(EnvCPUThreshold); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			c.CPUThreshold = f
		}
	}
	if v := os.Getenv(EnvRAMThreshold); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 && f <= 1 {
			c.RAMThreshold = f
		}
	}
	if v := os.Getenv(EnvSessionRetention); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.SessionRetention = d
		}
	}
	if v := os.Getenv(EnvBrowserMaxConcurrency); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			c.Upgrade.BrowserMaxConcurrency = n
		}
	}
	if c.CostModel == nil {
		c.CostModel = DefaultCostModel()
	}
	if c.DomainLimits == nil {
		c.DomainLimits = map[string]int{}
	}
	return c
}

func (c SchedulerConfig) Validate() error {
	if c.MaxGlobalConcurrency < 1 {
		return fmt.Errorf("MaxGlobalConcurrency must be >= 1")
	}
	if c.CPUThreshold <= 0 || c.CPUThreshold > 1 {
		return fmt.Errorf("CPUThreshold must be in (0,1]")
	}
	if c.RAMThreshold <= 0 || c.RAMThreshold > 1 {
		return fmt.Errorf("RAMThreshold must be in (0,1]")
	}
	if c.Upgrade.BrowserMaxConcurrency < 0 {
		return fmt.Errorf("BrowserMaxConcurrency must be >= 0")
	}
	if c.Retry.MaxAttempts < 0 {
		return fmt.Errorf("Retry.MaxAttempts must be >= 0")
	}
	if c.SessionRetention <= 0 {
		return fmt.Errorf("SessionRetention must be > 0")
	}
	if c.HysteresisWindow < 0 || c.Cooldown < 0 {
		return fmt.Errorf("HysteresisWindow and Cooldown must be >= 0")
	}
	for tt := range c.CostModel {
		if _, ok := model.TaskTypes[string(tt)]; !ok {
			return fmt.Errorf("CostModel contains unknown TaskType %q", tt)
		}
	}
	return nil
}

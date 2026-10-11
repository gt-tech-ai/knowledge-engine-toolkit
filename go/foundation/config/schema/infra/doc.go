// Package infra provides typed configuration structures for Tech AI Knowledge Engine
// services.
//
// Each struct corresponds to a section of the base config file (base.yaml) and supports:
//   - Default values via DefaultXConfig() factory functions
//   - Validation via Validate() methods
//   - Helper methods (DSN(), Addr(), etc.) for derived values
//
// Usage:
//
//	cfg := infra.DefaultDatabaseConfig()
//	// Override from Viper:
//	viper.UnmarshalKey("database", &cfg)
//	if err := cfg.Validate(); err != nil {
//	    log.Fatal(err)
//	}
//	db, err := sql.Open("postgres", cfg.DSN())
package infra

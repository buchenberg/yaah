package config

type Config struct {
	Port    int
	Debug   bool
	DBPath  string
	Verbose bool
}

func DefaultConfig() Config {
	return Config{
		Port:   8080,
		Debug:  false,
		DBPath: "data.db",
		Verbose: false,
	}
}

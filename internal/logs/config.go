package logs

type LogConfig struct {
	ConsoleLogRotateEnabled           bool
	ConsoleLogCheckFreqSec      int
	ConsoleLogFileSize       string
	ConsoleLogNumRotate      int
	ConsoleLogPath string
	ConsoleLogBackupPath string
	AggLogFileSize       string
	AggLogNumRotate      int
	RotateCheckFrequency 	int
	LogRotateFilePath		string
	LogRotateStateFilePath	string
}

func DefaultLogConfig() LogConfig {
	return LogConfig{
		ConsoleLogRotateEnabled:          true,
		ConsoleLogFileSize:      "5M",
		ConsoleLogNumRotate:     2,
		ConsoleLogPath: 	 "/var/log/conman",
		ConsoleLogBackupPath: "/var/log/conman.old",
		AggLogFileSize:      "20M",
		AggLogNumRotate:     1,
		RotateCheckFrequency: 600,
		LogRotateFilePath: "./logrotate.conman",
		LogRotateStateFilePath: "/tmp/rot_conman.state",
	}
}
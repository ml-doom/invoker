package internal

// This implementation is temporary until we won't find a proper way to supply the log cfg to invoker ([1] config.py, [2] env injection )


import (
	"encoding/json"
	"os"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/pkg/errors"
)



// SetAWSStreamPrefix sets awslogs-stream-prefix in LogOpts
func SetAWSStreamPrefix(c *container.LogConfig, num int) {
	c.Config["awslogs-stream-prefix"] = strconv.Itoa(num)
}

func ParseLogConfig(path string) (*container.LogConfig, error) {
	// parse json from path
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.WithMessage(err, "failed to open log config file")
	}

	defer file.Close()
	var cfg container.LogConfig

	if err := json.NewDecoder(file).Decode(&cfg); err != nil {
		return nil, errors.WithMessage(err, "failed to decode log config file")
	}

	return &cfg, nil
}

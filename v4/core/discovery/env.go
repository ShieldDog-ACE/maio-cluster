package discovery

import "os"

func lookupEnv(key string) (string, bool) { return os.LookupEnv(key) }

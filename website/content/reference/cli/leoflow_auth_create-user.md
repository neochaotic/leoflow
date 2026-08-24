---
title: "leoflow auth create-user"
linkTitle: "auth create-user"
weight: 14
---

Create a user on the control plane (admin only).

```
leoflow auth create-user [flags]
```

### Options

```
      --email string       email of the user to create
  -h, --help               help for create-user
      --password string    password for the new user
      --role stringArray   existing role to grant (repeatable); empty grants none
      --server string      control plane base URL (default: config server_url)
      --token string       admin JWT bearer token (default: config token)
```

### Options inherited from parent commands

```
      --config string       config file path (default ~/.leoflow/config.yaml)
      --log-level string    log level: debug, info, warn, error
      --server-url string   control plane API base URL
```

### SEE ALSO

* [leoflow auth](/reference/cli/leoflow_auth/)	 - Manage authentication tokens.


# clouddns

_clo-µ-ddns_ / _cloudflare ddns_

A lightweight, concurrent Dynamic DNS client specifically for Cloudflare.

## Overview

This DDNS client automatically updates your Cloudflare DNS records when your
public IP address changes. It supports both IPv4 (A records) and IPv6 (AAAA
records), making it perfect for home servers, self-hosted services, or any
system with a dynamic IP address that needs a consistent domain name. It's
designed around simplicity, efficiency, and reliability.

### Features

- **Maximally concurrent**: All operations that can be performed concurrently
  _are_ performed concurrently.
- **Lazy updating**: If a cache directory is provided, records are only updated
  when the public IP address has changed.
- **Structured logging**: JSON logging for easy parsing, monitoring, and
  troubleshooting.
- **Simple setup**: DNS configuration is a JSON file, runtime configuration is
  done via environment variables. No domain-specific languages.
- **Webhook notifications**: Optional webhook notifications on successful DNS
  updates, with built-in Discord support.

## Installation

I don't plan to release any binaries for this project. To build, using the Nix
setup is recommended.

This command will build the binary.

```bash
nix build
```

Use the Nix devshell to edit with all the tools available.

```bash
nix develop
go build
```

## Configuration

The client uses a JSON configuration file to specify which DNS records to
update.

> [!CAUTION]
> Credentials will be stored in plain text in the configuration file. It is
> important to keep this file secure; use permissions to restrict read access to
> the file.

### Configuration format

The top-level `a` and `aaaa` keys are both optional, as is the `webhooks` field
in each record.

```json
{
  "a": [
    {
      "name": "example.com",
      "api_token": "YOUR_CLOUDFLARE_API_TOKEN",
      "zone_id": "YOUR_ZONE_ID",
      "record_id": "YOUR_RECORD_ID"
    }
  ],
  "aaaa": [
    {
      "name": "example.com",
      "api_token": "YOUR_CLOUDFLARE_API_TOKEN",
      "zone_id": "YOUR_ZONE_ID",
      "record_id": "YOUR_RECORD_ID",
      "webhooks": ["https://discord.com/api/webhooks/examplewebhookjibberish"]
    }
  ]
}
```

TypeScript is the best language to describe the structure of JSON.

```typescript
type DNSRecord = {
  name: string;
  api_token: string;
  zone_id: string;
  record_id: string;
  webhooks?: string[];
};

type ConfigFile = {
  a?: DNSRecord[];
  aaaa?: DNSRecord[];
};
```

### DNSRecord parameters

Each record requires the following fields:

| Field       | Description                                                                                     | Required |
| ----------- | ----------------------------------------------------------------------------------------------- | -------- |
| `name`      | The fully qualified domain name for the record (e.g., `example.com` or `subdomain.example.com`) | Yes      |
| `api_token` | Your Cloudflare API token with permissions to edit DNS records                                  | Yes      |
| `zone_id`   | The Cloudflare Zone ID for your domain (found in the Cloudflare dashboard)                      | Yes      |
| `record_id` | The specific DNS record ID to update (found via Cloudflare API)                                 | Yes      |
| `webhooks`  | An optional array of webhook URLs to notify on successful updates (see Webhook section below)   | No       |

### Webhooks

The client can send notifications to webhook URLs when DNS records are
successfully updated. This is useful for monitoring, alerting, or triggering
other automation.

#### Standard Webhooks

For non-Discord webhook URLs, a JSON payload is sent with the following
structure:

```json
{
  "record_name": "example.com",
  "record_type": "A",
  "ip_address": "192.168.1.100"
}
```

#### Discord Webhooks

For Discord webhooks (URLs starting with `https://discord.com/api/webhooks/`),
only the IP address is sent as the message content for a cleaner appearance in
Discord channels.

#### Webhook Behavior

- Webhooks are called with a 10-second timeout
- Failed webhooks are retried up to 3 times
- Webhook failures do not prevent DNS updates from succeeding
- All webhooks for a record are called concurrently

### Cloudflare API Token Permissions

Your API token needs the following permissions:

- Zone → DNS → Edit

Each DNS record can use a different API token, which is useful for managing
multiple domains or when different tokens have different permission scopes.

### Finding your Cloudflare record IDs

You can find your Zone ID in the Cloudflare dashboard.

To find your Record ID, you can either view the network requests in the
Cloudflare dashboard (look for the API response for `dns_records`), or you can
use the Cloudflare API if you happen to have an API token with the DNS Read
permission:

```bash
curl -X GET "https://api.cloudflare.com/client/v4/zones/YOUR_ZONE_ID/dns_records" \
    -H "Authorization: Bearer YOUR_API_TOKEN" \
    -H "Content-Type: application/json"
```

## Usage

### Environment variables

The cache directory stores the last known IP addresses to avoid unnecessary API
calls to Cloudflare. This helps prevent rate limiting and reduces network
traffic. Set these environment variables before running:

| Variable           | Description                               | Required?        |
| ------------------ | ----------------------------------------- | ---------------- |
| `DDNS_CONFIG_PATH` | Path to your configuration JSON file      | Yes              |
| `DDNS_CACHE_PATH`  | Directory to store IP address cache files | No (recommended) |

The cache directory stores the last known IP addresses to avoid unnecessary API
calls to Cloudflare. This helps prevent rate limiting and reduces network
traffic.

### Running

```bash
export DDNS_CONFIG_PATH=/path/to/config.json
export DDNS_CACHE_PATH=/path/to/cache
clouddns
```

### Setting up as a scheduled task

#### NixOS example

This example uses agenix to store the configuration file.

```nix
{
  config,
  perSystem,
  ...
}:
{
  users.users.clouddns = {
    description = "System user for clouddns";
    isSystemUser = true;
    group = "clouddns";
  };

  users.groups.clouddns = { };

  age.secrets.clouddns-config = {
    file = ./clouddns-config.json.age;
    owner = "clouddns";
    group = "clouddns";
    mode = "400";
  };

  systemd.services.clouddns = {
    description = "Update Cloudflare DNS records with the current IP address";
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];

    serviceConfig = {
      Type = "oneshot";
      NoNewPrivileges = true;
      PrivateDevices = true;
      MemoryDenyWriteExecute = true;
      User = "clouddns";
      Group = "clouddns";
      Environment = [
        "DDNS_CONFIG_PATH=${config.age.secrets.clouddns-config.path}"
        "DDNS_CACHE_PATH=/var/tmp"
      ];
      ExecStart = "${perSystem.clouddns.default}/bin/clouddns";
    };
  };

  systemd.timers.clouddns = {
    description = "Timer for clouddns";
    wantedBy = [ "timers.target" ];

    timerConfig = {
      OnBootSec = "1m";
      OnCalendar = "*:0/10";
    };
  };
}
```

#### Cron example

```bash
*/15 * * * * DDNS_CONFIG_PATH=/path/to/config.json DDNS_CACHE_PATH=/path/to/cache /path/to/clouddns
```

#### Systemd timer example

Create a service file `/etc/systemd/system/clouddns.service`:

```ini
[Unit]
Description=Cloudflare DDNS Client
After=network.target

[Service]
Type=oneshot
Environment="DDNS_CONFIG_PATH=/path/to/config.json"
Environment="DDNS_CACHE_PATH=/tmp"
ExecStart=/path/to/clouddns

[Install]
WantedBy=multi-user.target
```

Create a timer file `/etc/systemd/system/clouddns.timer`:

```ini
[Unit]
Description=Run Cloudflare DDNS Client every 15 minutes

[Timer]
OnBootSec=1min
OnUnitActiveSec=15min
AccuracySec=1s

[Install]
WantedBy=timers.target
```

Enable and start the timer:

```bash
sudo systemctl enable clouddns.timer
sudo systemctl start clouddns.timer
```

## How it works

1. The client fetches your current public IP address from external services:
   - IPv4 addresses from [api.ipify.org](https://api.ipify.org/)
   - IPv6 addresses from [api6.ipify.org](https://api6.ipify.org/)
2. It compares this with the cached IP address for each configured DNS record
3. If a record's IP has changed (or was never cached), it updates that specific
   DNS record via the Cloudflare API
4. Upon successful update, it caches the new IP address for future comparison
   and sends webhook notifications if configured
5. Each record is tracked independently and processed concurrently, so changing
   record configurations or failed updates only affect the specific records
   involved
6. A and AAAA records are processed simultaneously for maximum efficiency

## License

This project is licensed under the MIT License - see the LICENSE file for
details.

## Contributing

This project is essentially feature complete. I'm happy to take supplemental
additions, such as configuration examples, updated documentation, etc. However,
if you have an idea for something you'd like to add, you're free to fork the
project and add it in your own copy! If you think it fits in with the goals of
this project, please do open a Pull Request and let's chat about it.

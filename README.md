# Pupirka

(Simple Backup over SSH and Telnet)
---

Author @vasilevkirill [My Web Site](https://vasilevkirill.ru)

Pupirka is a Application for simple execute command on a network device and response save in file, supported SSH LocalPort Forward. A device is reached over SSH by default, or over Telnet when `"protocol": "telnet"` is set.

 - Pupirka - this not service
 - Pupirka - a single binary with a few small Go modules (see `go.mod`)
 - Pupirka - running one binary and save backup from all MikroTik, Cisco, Eltex, TP-Link or other devices
 - Pupirka - Supported all Operating System if supported Golang

## Contents

- [Pupirka](#pupirka)
    - [Contents](#Contents)
    - [Installation](#instalation)
        - [Download](#download)
        - [Build From source](#build-from-source-code)
        - [First setup](#first-setup)
    - [Quick Start](#quick-start)
    - [Device Settings](#device-all-parameter)
      - [Device Hook](#device-hook)
      - [Telnet](#telnet)
    - [Config Pupirka](#puprik-config)
      - [Device Default Parameter](#devicedefault)
      - [Logging](#log)
      - [Path](#path)
      - [Proccess](#proccess)
      - [Global Hooks](#global-hooks)
    - [Time Format](#time-format)
---

## Installation
### Download

For download your need used this [link](https://github.com/vasilevkirill/pupirka/releases)
Save binary for your Operating System

### Build from source code
1. Get source

You need use git and clone repository in your system

```bash
git clone https://github.com/vasilevkirill/pupirka.git
```
2. Compily from source

Your need installed go and running command
```bash
cd pupirka
go get -d ./...
go build -ldflags "-s" -o pupirka .
```

your binary set name pupirka if your used nix*like set execute permission `chmod +x pupirka`


## First setup

If your first run pupirka, needed running once

```bash
./pupirka
```

Pupirka automated Created required directories and config file `pupirka.config.json`.

Needed added `json` file in folder `device`. File name is name device. (Example `mydevice-01.json` Pupirka used file name as device name `mydevice-01`)

```json
{
  "address": "111.222.223.222",
  "username": "backupuserssh",
  "password": "asdlmhas,o9duajsp9odj"
}
```

Paprika connected to SSH server in address and auth used credential from `json` file.

Running command `/export` (Default) and saved response in file `./backup/mydevice-01/20200903T2210.rsc` if you need automated backup used cron in unix or scheduler in Windows, running `pupirka`.

## Device all parameter

```json
{
  "address": "111.222.223.222",
  "protocol": "telnet",
  "porttelnet": 23,
  "username": "backupuserssh",
  "password": "asdlmhas,o9duajsp9odj",
  "portssh": 22,
  "timeout": 10,
  "commandtimeout": 60,
  "idletimeout": 10,
  "every": 3600,
  "rotate": 730,
  "parent": "mydevice-02",
  "prompt": "\\[admin@MikroTik\\] >",
  "initcommand": "enable",
  "enablepassword": "",
  "timeformat": "20060102T1504",
  "prefix": "rootR1-",
  "filenameformat": "%p%t.rcs",
  "key":"blabla.key",
  "clearstring":"",
  "hook": {
      "backup": "",
      "error": "",
      "skip": ""
    }
}
```

All supported parameter in `json` file for device

 - `portssh` - if your device running or access not 22 (default) port ssh, set needed tcp port.
 - `protocol` - how to reach the device: `"ssh"` (default) or `"telnet"`. Device files without this field keep working over SSH.
 - `porttelnet` - tcp port for telnet, 23 by default.
 - `timeout` - if your device very slow response, set needed time wait in second. Used for connecting and for the login dialogue.
 - `commandtimeout` - how long to wait for the command output (seconds). Default is `timeout`. Telnet only.
 - `idletimeout` - how long to wait for any byte from the device before saving what arrived (seconds). Default is `timeout`. Telnet only.
 - `every` - how often to backup, check the time of the last file and if the difference with the current time is greater than the time of the file, a backup will be created.
 - `rotate` - delete old files backups older than days. (in order not to accidentally delete all backups, the deletion will occur if ONLY the count of backup files is more than (5) five files.)
 - `command` - execute command on remote device, if you need backup only firewall in mikrotik (`/ip firewall export`). For telnet the command is typed into an interactive session, so vendor syntax applies (`show running-config` on Cisco/Eltex, `show running-config` or `/export` on MikroTik).
 - `prompt` - telnet only: regular expression of the device prompt, for example `"\\[admin@.*\\] >"`. Empty value uses built-in patterns (MikroTik, Cisco, Eltex, TP-Link, BDCOM style).
 - `initcommand` - telnet only: preparation commands executed after login, before `command`. Several commands can be separated with `;`, for example `"enable;terminal length 0"`.
 - `enablepassword` - telnet only: password to answer an `enable` password prompt caused by `initcommand`.
 - `parent` - if your devices are behind the device and are not directly accessible, you can use this parameter. Specify the name of the device through which you want to establish a connection. The parent device itself must be reachable over SSH; the tunnel works for telnet devices too.
 - `timeformat` - as generate time string in file backup. ([Read documentation and example](#time-format))
 - `prefix` - adding prefix in file name backups.
 - `filename` - position `prefix` and `time` in file name. `%p` replaced `prefix` and `%t` replaced current time, %n replaced device name
 - `key` - Private Key for used SSH Authorized, need saved private key in folder `./keys`. (**if need used SSH keys, need `password` filed delete or set `""`**). Telnet devices ignore this field.
 - `clearstring` - a regular expression: every line **starting** with it is removed from the backup. Several prefixes can be joined with `|`. Devices that re-encrypt passwords or SNMP communities on every read need this, otherwise each backup differs from the previous one: `"clearstring": "username admin password|.*\\*\\*/\\*"`.
 - `hook` - Execute command in os.

Many defaults can be changed globally in the config file `pupirka.config.json`.

### Devece Hook

A device can have three (3) states.

 - `skip` if no need backup, if time last backup less than `every` (timefilelastbackup < every)
 - `error` if when doing backup device return error (connected ssh, connected parent, execute command, create file and etc...)
 - `backup` Good job, backup successfully

this string can be used next arguments
  - `%name` - name device (from filename).
  - `%parent`- Parent if connected via ssh ssh port forward.
  - `%filename` - filename backup
  - `%address` - Address Device
  - `%portssh` - Port ssh Device

Pupirka **NOT** Wait execute command for devices.

### Telnet

Some devices have SSH disabled or do not support it at all. Set `"protocol": "telnet"` in the device file to back them up over Telnet:

```json
{
  "address": "192.0.2.10",
  "protocol": "telnet",
  "porttelnet": 23,
  "username": "admin",
  "password": "secret",
  "command": "show running-config",
  "initcommand": "enable;terminal length 0",
  "commandtimeout": 60,
  "idletimeout": 15
}
```

How a Telnet session runs:

1. connect to `address:porttelnet` (23 by default);
2. wait for `Login:`, `User:`, `User Name:` or `Username:` and `Password:`, answer from the device file;
3. run every `initcommand` (separated by `;`) and wait for the prompt again;
4. send `command` and read until the prompt;
5. strip the command echo, ANSI escapes, pager markers and the prompt, save the rest.

The Telnet transport is built into pupirka (no external module): it removes IAC command sequences from the stream and escapes `0xFF` when writing, and it **does not answer** option negotiation. That is deliberate - some OLTs (FD1304E-B1 and relatives) accept the login, then drop the session with `EOF` on the first command as soon as the client agrees to `ECHO`/`SGA`/`NAWS`. Reference clients do not negotiate either, and every device tested works without it.

The end of the output is detected by the prompt. Without a `prompt` field the built-in patterns are used; they cover MikroTik (`[admin@MikroTik] >`), Cisco (`Router>`, `Router#`, `Router(config)#`), Eltex, TP-Link and BDCOM (`hostname#`) style prompts. A lone `#` that TP-Link and Eltex print as a section separator is never taken for a prompt. If the device prompt does not match, pupirka writes a warning to the device log and saves what arrived when `idletimeout` fires, so set `prompt` explicitly in that case.

Long output (`--More--`, `<--- More --->`, `Press any key to continue (Q to quit)`) is answered automatically with a space, also when the marker is split between two TCP packets. Disabling paging in `initcommand` (`terminal length 0` on Cisco/Eltex/BDCOM) is still faster and more reliable.

**Security warning:** Telnet sends the login, the password and the configuration **unencrypted**. Anyone on the path can read them. Use Telnet only on a management network you trust, or keep SSH where possible. Prefer `parent` (an SSH tunnel) when the Telnet device is far away.

Limitations:

 - `key` is ignored for telnet devices (a warning is written to the log) - Telnet has no key authentication;
 - `parent` works only if the parent device itself is reachable over SSH;
 - prompt detection is a heuristic: a wrong `prompt` gives either a warning (idle timeout) or a `commandtimeout` error;
 - `commandtimeout` covers the whole command phase, `timeout` covers connect and login;
 - devices that answer `\r\n` with a stray `^` are fine (pupirka sends a single CR), but a device that needs a different line ending cannot be configured yet.

Troubleshooting a Telnet device:

 - set `"level": "debug"` in `log.level` of `pupirka.config.json` and read `log/<device>/device.log`;
 - the log shows the login steps, the `initcommand` calls, whether the prompt matched and how many bytes arrived;
 - `TelnetClientRun: prompt not matched ... saving N bytes as is` means the output was saved but the prompt pattern does not fit the device - fix `prompt`;
 - `commandtimeout exceeded` means the device never returned a prompt: increase `commandtimeout` or fix `prompt`.

## Config Pupirka

If pupirka execute and file `pupirka.config.json` not found, pupirka created config and set all default parameter

format: json

```json
{
  "devicedefault": {
    "clearstring":"",
    "command": "/export",
    "commandtimeout": 0,
    "every": 3600,
    "filenameformat": "%p%t.rcs",
    "idletimeout": 0,
    "initcommand": "",
    "enablepassword": "",
    "key": "",
    "portshh": 22,
    "portssh": 22,
    "porttelnet": 23,
    "prefix": "",
    "prompt": "",
    "protocol": "ssh",
    "rotate": 730,
    "timeformat": "20060102T1504",
    "timeout": 10,
    "hook": {
        "backup": "",
        "error": "",
        "skip": ""
      }
  },
  "git": {
      "branch": "master",
      "password": "",
      "user":"Pupirka",
      "email": "vk@mikrotik.me",
      "remote": ""
   },
  "log": {
    "format": "text",
    "maxday": 1
  },
  "path": {
    "backup": "./backup",
    "devices": "./device",
    "key": "./keys",
    "log": "./log"
  },
  "process": {
    "max": 10
  },
  "global": {
    "hook": {
      "post": "",
      "pre": ""
    },
    "onlyone": false
  }
}
```

Sub Categories:
  - `devicedefault` - default parameter used if not set in devices json file config
  - `log` - global parameter for logging
  - `path` - directories
  - `process` - limits for pupirka

### devicedefault
  When Pupirka reads device parameters, if in devices json files not set parameter Pupirka used default value from config. Even if you did not set in the global config, these parameters and values are stored in the code Pupirka.

### git
 Clone current remote repo, Commit and Push remote

  - `branch` - need isset
  - `remote` - url to remote repo

### log
  - `format` - the logging format can be `json` or `text`.
  - `maxday` - how many days can be recorded in one file.

### Path

Pupirka created all folder if not found.

 - `backup` - folder for save backup
 - `devices` - folder used for find your devices. Your needed create json file devices in this folder.
 - `key` -  Pupirka find in this folder Private Keys for Auth devices.
 - `log` -  Folder for save all log.

### Process
  - `max` - in order not to overload the CPU, Pupirka it will back up in groups of the specified count.

### Global
 other parameter
 - `onlyone` true or false. Running only one application pupirka

#### Global Hooks
   Running and wait execute custom command.
   - `pre` running before backups
   - `post` running after all backups    

## Time Format

Used specified Golang format

 - Year - `2006` = result `2020`, `06` = result `20`
 - Mount - `01` = result `05`, `1` = result `5`
 - Day - `02` = result `07`, `2` = result `7`
 - Hours - `03` = result `05`, `3` = result `5`, `15` = result `17`
 - Minutes - `04` = result `09`, `4` = result `9`
 - Seconds - `05` = result `07`, `5` = result `7`

Examples: datetime 2020-09-04:22:15:01

```json
`timefomat` = "20060102T1504" #result 20200904T2215 this default
 ```

```json
`timefomat` = "2006y01m02T15h04m" #result 2020y09m04T22h15m
 ```

```json
`timefomat` = "2006-01-02 150405" #result 2020-09-04 221501
```

```json
`timefomat` = "2006-01-2 150405" #result 2020-09-4 221501
```

```json
`timefomat` = "2006-01-2 30405" #result 2020-09-4 101501
```
Please read documentation golang for time format loyuot


---
RUS - https://vasilevkirill.ru/articles/pupirka/

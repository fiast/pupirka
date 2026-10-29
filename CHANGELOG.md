# Changelog
## v0.8 (2026-10-30)
**Implemented enhancements:**
- add telnet support: device parameter `"protocol": "telnet"`, ssh remains the default
- add device parameters `protocol`, `porttelnet`, `prompt`, `initcommand`, `enablepassword`, `idletimeout`, `commandtimeout`
- add automatic pager handling for telnet (`--More--`, `<--- More --->`, `---- More ( Press 'Q' to break ) ----`, `Press any key`)
- add a built-in telnet transport (IAC stripping, no option negotiation) - no new external module
- add debug tracing of the telnet dialogue with password masking
- add unit tests: login dialogue, prompt detection, output cleaning, protocol dispatch, tunnel lifecycle, telnet wire format

**Fixed bugs:**
- ssh port forward: panic when the ssh connection to the parent failed
- ssh port forward: readiness race between the local listener and the first dial
- ssh port forward: listener and port leak after the backup
- `SshAddressFormat` is now `DeviceAddressFormat` and honours the resolved port
- `fmt.Sprintf(err.Error())` in `ScanDevice` (go vet: non-constant format string)

**Notes:**
- `key` is not supported for telnet devices and is ignored with a warning
- telnet traffic is not encrypted, see the security warning in the README
- verified on TP-Link T2600G-28TS (`User:` prompt, `Press any key to continue (Q to quit)` pager, lone `#` section separators), BDCOM P3310B (`--More--` pager, `enable` + `terminal length 0`) and FD1304E-B1 / other EPON OLTs (`User name:` prompt, `---- More ( Press 'Q' to break ) ----` pager, single-session limit)

## v0.7 (2020-02-02)
**Implemented enhancements:**
- add git remote save push and pull
- add onlyone parameter 

## v0.6 (2020-01-27)
**Implemented enhancements:**
- add ssh keys Algo diffie-hellman-group1-sha1,diffie-hellman-group14-sha1,ecdh-sha2-nistp256,ecdh-sha2-nistp384,ecdh-sha2-nistp521,diffie-hellman-group-exchange-sha1,diffie-hellman-group-exchange-sha256,curve25519-sha256@libssh.org
- add ssh chippers aes128-gcm@openssh.com,cast128-cbc,aes128-ctr,aes192-ctr,aes256-ctr,3des-cbc,blowfish-cbc,twofish-cbc,twofish256-cbc,twofish192-cbc,twofish128-cbc,aes256-cbc,aes192-cbc,aes128-cbc,arcfour

**Fixed bugs:**
- calculate day in rotate backup
- remove old backup file in rotate

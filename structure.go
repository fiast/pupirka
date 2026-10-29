package main

import (
	"github.com/sirupsen/logrus"
)

type Device struct {
	Name           string         `json:"-"`
	Address        string         `json:"address"`
	PortSSH        uint16         `json:"portssh"`
	PortTelnet     uint16         `json:"porttelnet"`
	Protocol       string         `json:"protocol"`
	Username       string         `json:"username"`
	Password       string         `json:"password"`
	Key            string         `json:"key"`
	Timeout        int            `json:"timeout"`
	Every          int            `json:"every"`
	Rotate         int            `json:"rotate"`
	Command        string         `json:"command"`
	Parent         string         `json:"parent"`
	Prompt         string         `json:"prompt"`
	InitCommand    string         `json:"initcommand"`
	EnablePassword string         `json:"enablepassword"`
	IdleTimeout    int            `json:"idletimeout"`
	CommandTimeout int            `json:"commandtimeout"`
	Prefix         string         `json:"prefix"`
	TimeFormat     string         `json:"timeformat"`
	FileNameFormat string         `json:"filenameformat"`
	Clearstring    string         `json:"clearstring"`
	DeviceHooks    DeviceHook     `json:"hook"`
	Dirbackup      string         `json:"-"`
	Port           uint16         `json:"-"`
	Lastbackup     string         `json:"-"`
	Authkey        bool           `json:"-" default:"false"`
	BackupFileName string         `json:"-"`
	StatusJob      string         `json:"-"`
	Logdevice      *logrus.Logger `json:"-"`
}
type DeviceList struct {
	Devices []Device
}

type DeviceHook struct {
	Backup string `json:"backup"`
	Skip   string `json:"skip"`
	Error  string `json:"error"`
}

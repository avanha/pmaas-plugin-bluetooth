module pmaas.io/plugins/bluetooth

go 1.22

toolchain go1.22.2

require (
	github.com/muka/go-bluetooth v0.0.0-20240701044517-04c4f09c514e
	pmaas.io/spi v0.0.0
)

require (
	//tinygo.org/x/bluetooth v0.5.0
	//github.com/go-ble/ble v0.0.0-20220207185428-60d1eecf2633
	github.com/fatih/structs v1.1.0 // indirect
	github.com/godbus/dbus/v5 v5.1.0
	github.com/konsorten/go-windows-terminal-sequences v1.0.3 // indirect
	github.com/sirupsen/logrus v1.6.0 // indirect
	golang.org/x/sys v0.0.0-20200728102440-3e129f6d46b1 // indirect
)

replace pmaas.io/spi => ../../pmaas-spi

// replace github.com/muka/go-bluetooth => ../../go-bluetooth

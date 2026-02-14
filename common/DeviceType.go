package common

type DeviceType int

const (
	Unknown DeviceType = iota
	Thermometer
	ThermometerAndHygrometer
)

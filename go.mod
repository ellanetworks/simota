module github.com/ellanetworks/simota

go 1.26.5

require (
	github.com/ellanetworks/smsc/smpp v0.0.0-00010101000000-000000000000
	github.com/mattn/go-sqlite3 v1.14.52 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

replace github.com/ellanetworks/smsc/smpp => ../smsc/smpp

// Package model holds the data types shared across Garrison's layers.
//
// It is a leaf package: it must not import any other Garrison package. Every
// other layer may import it. internal/arch enforces this.
//
// The point of the rule is that internal/games (which describes what a server
// should be) and internal/host (which makes it so) both speak these types and
// neither one imports the other.
package model

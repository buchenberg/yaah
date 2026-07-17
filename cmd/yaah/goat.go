package yaah

import "github.com/buchenberg/yaah/internal/banner"

// The yaah goat. Running `yaah yaah` prints an ASCII goat; each extra
// "yaah" argument escalates the celebration, capping at the final level.
// This is an easter egg: it is checked by arg inspection in runRoot
// rather than registered as a cobra command, so it never appears in
// help output and stays off the agent hot path.

// goatLevel pairs a crowd chant with goat art of escalating enthusiasm.
type goatLevel struct {
	chant string
	art   string
}

// goatLevels holds the celebration stages. Level N is used when the user
// passes N "yaah" arguments; counts beyond the last level reuse it.
var goatLevels = []goatLevel{
	{
		chant: "yaah!",
		art: `        _))
       > *\
        ;'\\__
          |    \
          ||--||
          ^^  ^^`,
	},
	{
		chant: "yaah! yaah!",
		art: `         \o/
        _))
       > ^\      _~
        ;'\\__--'  \_
          | )   _   \ \
         / /   ''    w w
        w w`,
	},
	{
		chant: "YAAH! YAAH! YAAH!",
		art: `   *  .  *  .  *  .  *  .  *
      \o/    _))    \o/
            > ^\      _~
        ~~~~;'\\__--'  \_
    jump!     | )   _   \ \
             / /   ''    w w
            w w
   .  *  .  *  .  *  .  *  .`,
	},
}

// isAllYaahs reports whether args is a non-empty list consisting only of
// the literal word "yaah" — the trigger for the goat easter egg.
func isAllYaahs(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, a := range args {
		if a != "yaah" {
			return false
		}
	}
	return true
}

// goatCelebration renders the goat for count "yaah" arguments. Enthusiasm
// escalates with count and caps at the last level in goatLevels.
func goatCelebration(count int) string {
	if count < 1 {
		count = 1
	}
	if count > len(goatLevels) {
		count = len(goatLevels)
	}
	level := goatLevels[count-1]
	return "  " + Bold(level.chant) + "\n\n" + banner.Lolcat(level.art) + "\n"
}

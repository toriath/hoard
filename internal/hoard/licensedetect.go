package hoard

import (
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ---- licence detection ----

// detectPackLicense reads the descriptive files in the archive's root and
// looks for a licence and an attribution line. LICENSE and COPYING files are
// decided first; only without a result do READMEs, credits and other .txt/.md
// files count. conflict lists the licences found when they disagree (then no
// licence is returned).
func (s *Service) detectPackLicense(entries []zipEntry) (license, attribution, source string, conflict []string) {
	type doc struct {
		e    zipEntry
		rank int
	}
	var docs []doc
	for _, e := range entries {
		if len(e.parts) != 1 {
			continue
		}
		stem := strings.TrimSuffix(e.base(), filepath.Ext(e.base()))
		low := strings.ToLower(e.base())
		switch {
		case reLicenseName.MatchString(stem):
			docs = append(docs, doc{e, 0})
		case reDocName.MatchString(stem):
			docs = append(docs, doc{e, 1})
		case strings.HasSuffix(low, ".txt") || strings.HasSuffix(low, ".md"):
			docs = append(docs, doc{e, 2})
		}
	}
	sort.SliceStable(docs, func(i, j int) bool { return docs[i].rank < docs[j].rank })
	if len(docs) > 8 {
		docs = docs[:8]
	}
	found := map[int]map[string]string{} // rank -> licence -> file
	for _, d := range docs {
		if d.e.f.UncompressedSize64 > 2<<20 {
			continue
		}
		rc, err := d.e.f.Open()
		if err != nil {
			continue
		}
		b, _ := io.ReadAll(io.LimitReader(rc, importDocLimit))
		rc.Close()
		text := strings.ToValidUTF8(string(b), "")
		for _, l := range DetectLicenses(text) {
			if found[d.rank] == nil {
				found[d.rank] = map[string]string{}
			}
			if _, ok := found[d.rank][l]; !ok {
				found[d.rank][l] = d.e.base()
			}
		}
		if attribution == "" {
			attribution = DetectAttribution(text)
		}
	}
	for _, rank := range []int{0, 1, 2} {
		if len(found[rank]) == 0 {
			continue
		}
		var all []string
		for l := range found[rank] {
			all = append(all, l)
		}
		all = dropVersionless(all)
		sort.Strings(all)
		if len(all) == 1 {
			return all[0], attribution, found[rank][all[0]], nil
		}
		return "", attribution, "", all
	}
	return "", attribution, "", nil
}

var (
	reMIT      = regexp.MustCompile(`\bmit licen[sc]e\b`)
	reCCURL    = regexp.MustCompile(`creativecommons\.org/licenses/([a-z\-]+)/(\d\.\d)`)
	reCCZero   = regexp.MustCompile(`creativecommons\.org/publicdomain/zero|\bcc0[\s\-_]?1\.0|creative\s+commons\s+zero|\bcc0\s+universal|\bcc[\s\-_]zero|\blicen[sc]e[ds]?\s*(?:is|:|-|=|under|as)?\s*(?:the\s+)?cc0\b|\bcc0\s+licen[sc]e`)
	reCCMark   = regexp.MustCompile(`creativecommons\.org/publicdomain/mark`)
	reCCLong   = regexp.MustCompile(`(?:creative\s+commons\s+)?attribution((?:[\s\-]*(?:non-?commercial|share-?alike|no-?deriv(?:ative)?s?))*)[\s\-]*(\d\.\d)\s+(?:international|unported|generic|license|licence)`)
	reCCLongCC = regexp.MustCompile(`creative\s+commons\s+attribution((?:[\s\-]*(?:non-?commercial|share-?alike|no-?deriv(?:ative)?s?))*)(?:[\s,\-]*(?:license|licence))?[\s,\-]*(?:(?:version|v)?\s*(\d\.\d))?`)
	reCCShort  = regexp.MustCompile(`\bcc[\s\-_]?by((?:[\s\-_]?(?:nc|sa|nd))*)[\s\-_]*v?(\d\.\d)?`)
	reOFL      = regexp.MustCompile(`\b(?:sil\s+)?open\s+font\s+licen[sc]e`)
	reNegation = regexp.MustCompile(`\b(?:not|no|never|isn't|isnt|non)\s+(?:\S+\s+){0,2}$`)
	// The text of every Creative Commons licence carries a sentence about the
	// licence text itself; it says nothing about the licence of the pack.
	reCCBoilerplate = regexp.MustCompile(`cc0\s+public\s+domain\s+dedication`)
)

// negated reports whether the words before pos negate what follows ("not
// public domain", "NOT CC0").
func negated(low string, pos int) bool {
	from := max(0, pos-30)
	if i := strings.LastIndexAny(low[from:pos], ".\n"); i >= 0 {
		from += i + 1
	}
	return reNegation.MatchString(low[from:pos])
}

func ccID(mods, ver string) string {
	id := "CC-BY"
	m := strings.ToLower(mods)
	for _, k := range []struct{ word, tag string }{
		{"noncommercial", "NC"}, {"non-commercial", "NC"}, {"nc", "NC"},
		{"sharealike", "SA"}, {"share-alike", "SA"}, {"sa", "SA"},
		{"noderiv", "ND"}, {"noderivs", "ND"}, {"noderivatives", "ND"}, {"no-deriv", "ND"}, {"nd", "ND"},
	} {
		if containsWord(m, k.word) && !strings.Contains(id, "-"+k.tag) {
			id += "-" + k.tag
		}
	}
	if ver != "" {
		id += "-" + ver
	}
	return id
}

// DetectLicenses recognises common licences by their title, wording or
// creativecommons.org link and returns every distinct one found, as SPDX-style
// identifiers. Specific licences are decided by title or link; CC0 only by its
// own title or link, never by the dedication sentence found in the text of
// every Creative Commons licence; mentions that are negated ("not public
// domain") do not count.
func DetectLicenses(text string) []string {
	low := strings.ToLower(text)
	low = reCCBoilerplate.ReplaceAllString(low, " ")
	set := map[string]bool{}
	add := func(re *regexp.Regexp, f func(m []string) string) {
		for _, loc := range re.FindAllStringSubmatchIndex(low, 20) {
			if negated(low, loc[0]) {
				continue
			}
			m := make([]string, len(loc)/2)
			for k := range m {
				if loc[2*k] >= 0 {
					m[k] = low[loc[2*k]:loc[2*k+1]]
				}
			}
			if id := f(m); id != "" {
				set[id] = true
			}
		}
	}
	add(reCCURL, func(m []string) string {
		switch m[1] {
		case "by", "by-sa", "by-nc", "by-nd", "by-nc-sa", "by-nc-nd":
			return "CC-" + strings.ToUpper(m[1]) + "-" + m[2]
		}
		return ""
	})
	add(reCCLong, func(m []string) string { return ccID(m[1], m[2]) })
	add(reCCLongCC, func(m []string) string { return ccID(m[1], m[2]) })
	add(reCCShort, func(m []string) string { return ccID(m[1], m[2]) })
	add(reCCZero, func([]string) string { return "CC0-1.0" })
	add(reCCMark, func([]string) string { return "Public Domain" })
	add(reOFL, func([]string) string { return "OFL-1.1" })
	add(reMIT, func([]string) string { return "MIT" })
	if i := strings.Index(low, "permission is hereby granted, free of charge"); i >= 0 && !negated(low, i) {
		set["MIT"] = true
	}
	if i := strings.Index(low, "apache license"); i >= 0 && strings.Contains(low, "2.0") && !negated(low, i) {
		set["Apache-2.0"] = true
	}
	if len(set) == 0 { // the generic phrase only counts when nothing more specific is there
		if i := strings.Index(low, "public domain"); i >= 0 && !negated(low, i) {
			set["Public Domain"] = true
		}
	}
	out := make([]string, 0, len(set))
	for l := range set {
		out = append(out, l)
	}
	out = dropVersionless(out)
	sort.Strings(out)
	return out
}

// dropVersionless removes "CC-BY" when "CC-BY-4.0" is also present.
func dropVersionless(ids []string) []string {
	var out []string
	for _, id := range ids {
		covered := false
		for _, other := range ids {
			if other != id && strings.HasPrefix(other, id+"-") {
				if rest := other[len(id)+1:]; rest != "" && rest[0] >= '0' && rest[0] <= '9' {
					covered = true
				}
			}
		}
		if !covered {
			out = append(out, id)
		}
	}
	return out
}

// DetectLicense returns the licence a text names, or "" when it names none or
// several different ones.
func DetectLicense(text string) string {
	if l := DetectLicenses(text); len(l) == 1 {
		return l[0]
	}
	return ""
}

func containsWord(hay, word string) bool {
	for i := 0; ; {
		j := strings.Index(hay[i:], word)
		if j < 0 {
			return false
		}
		a, b := i+j, i+j+len(word)
		isLetter := func(c byte) bool { return c >= 'a' && c <= 'z' }
		if (a == 0 || !isLetter(hay[a-1])) && (b == len(hay) || !isLetter(hay[b])) {
			return true
		}
		i = a + 1
	}
}

var reAttribution = regexp.MustCompile(`(?im)^[\s\-*#>]*((?:(?:attribution|credits?|author|artist|created by|made by|copyright)\b|\(c\)|©).{0,200}|by\s+\S.{0,160})$`)

// DetectAttribution returns the first line of a text that reads like an
// attribution ("Created by …", "Author: …", "Copyright …", "© …", "by …").
func DetectAttribution(text string) string {
	for _, m := range reAttribution.FindAllStringSubmatch(text, 20) {
		line := strings.TrimSpace(m[1])
		// A bare label such as "Credits:" or "Attribution" says nothing.
		if i := strings.IndexAny(line, ":"); i >= 0 && strings.TrimSpace(line[i+1:]) == "" {
			continue
		}
		if len(strings.Fields(line)) < 2 {
			continue
		}
		return truncateRunes(line, 200)
	}
	return ""
}

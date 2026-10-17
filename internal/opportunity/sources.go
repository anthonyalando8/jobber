package opportunity

// Whether a posting's absence from a source means anything depends entirely on what that source
// returns when it is read.
//
// A source enumerates if one successful read returns the complete set of postings it currently
// advertises. Read such a source today and compare it with yesterday, and a posting that has
// disappeared has been taken down.
//
// Most sources here are not like that:
//
//   - brightermonday.co.ke and myjobmag.co.ke are read as keyword searches, one page of results
//     per term. A posting leaves that result set when newer postings push it off the first page,
//     or when the site reorders its own ranking. It is still open; it is just no longer on page
//     one. Concluding "no longer hiring" from this would mislabel live vacancies as dead, which
//     is the one error this whole mechanism must not make.
//   - weworkremotely is read as category RSS. An RSS feed carries the most recent N items, so an
//     older posting falls out of it while remaining perfectly open.
//
// EnumerableSources therefore lists only the sources whose single request is documented to return
// their whole current listing. The default is to be absent from this list: a source wrongly
// listed here suppresses live work, whereas a source wrongly left out only means the sweep misses
// some dead postings, which an operator can still suppress by hand.
//
// Himalayas is deliberately not listed. Its API returns a set in one request with no total count
// and no pagination handling in our adapter, so there is no way from here to tell a complete
// listing from a truncated one.
var EnumerableSources = []string{"remoteok", "remotive"}

// SourceEnumerates reports whether absence from this source is evidence that a posting ended.
func SourceEnumerates(source string) bool {
	for _, candidate := range EnumerableSources {
		if candidate == source {
			return true
		}
	}
	return false
}

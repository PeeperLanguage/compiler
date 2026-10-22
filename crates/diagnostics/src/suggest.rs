//! "Did you mean ...?": the known name closest to a misspelled one.

/// The candidate closest to `query`, ignoring case, but only when the typo
/// signal is strong enough and no other candidate is about as close. A lower
/// priority wins a tie between equally close candidates; callers without a
/// preference give every candidate the same priority.
pub fn nearest_name<'a>(
    query: &str,
    candidates: impl IntoIterator<Item = (&'a str, i32)>,
) -> Option<&'a str> {
    if query.is_empty() {
        return None;
    }
    let query = query.to_lowercase();
    // A name that differs in length by more than this can neither be taken
    // nor stop the best one from being taken (see `is_close_enough` and
    // `clearly_better_than`), so it is not measured.
    let too_far = most_edits(query.len()) + 1;
    let mut lower = String::new(); // one buffer for every candidate
    let mut best: Option<Match> = None;
    let mut second: Option<Match> = None;
    for (name, priority) in candidates {
        // lower case keeps an ASCII name's length; another may change it
        let ascii = name.is_ascii();
        if ascii && name.len().abs_diff(query.len()) > too_far {
            continue;
        }
        if name.trim().is_empty() {
            continue;
        }
        lower.clear();
        lower.push_str(name);
        if ascii {
            lower.make_ascii_lowercase();
        } else {
            lower = lower.to_lowercase();
        }
        let candidate = Match {
            name,
            distance: levenshtein(query.as_bytes(), lower.as_bytes()),
            shared_prefix: query
                .bytes()
                .zip(lower.bytes())
                .take_while(|(a, b)| a == b)
                .count(),
            length_difference: query.len().abs_diff(lower.len()),
            priority,
        };
        if best.as_ref().is_none_or(|b| candidate.ranks_before(b)) {
            second = best.replace(candidate);
        } else if second.as_ref().is_none_or(|s| candidate.ranks_before(s)) {
            second = Some(candidate);
        }
    }
    let best = best?;
    // An exact match of a one- or two-letter query is taken even when others tie.
    let exact_short = query.len() <= 2 && best.distance == 0;
    let accepted = exact_short
        || best.is_close_enough(query.len()) && second.is_none_or(|s| best.clearly_better_than(&s));
    accepted.then_some(best.name)
}

struct Match<'a> {
    name: &'a str,
    distance: usize,
    shared_prefix: usize,
    length_difference: usize,
    priority: i32,
}

impl Match<'_> {
    /// The last key is the name, so the answer does not depend on the order
    /// the candidates come in.
    fn ranks_before(&self, other: &Match) -> bool {
        (
            self.distance,
            std::cmp::Reverse(self.shared_prefix),
            self.priority,
            self.length_difference,
            self.name,
        ) < (
            other.distance,
            std::cmp::Reverse(other.shared_prefix),
            other.priority,
            other.length_difference,
            other.name,
        )
    }

    /// Whether the edit distance is small for a query of `query_len` bytes.
    fn is_close_enough(&self, query_len: usize) -> bool {
        let longer = query_len.max(self.name.len());
        self.distance <= most_edits(query_len)
            && match query_len {
                0..=2 => self.distance == 1 && self.shared_prefix > 0,
                3..=4 => true,
                5..=7 => self.distance * 5 <= longer * 2,
                _ => self.distance * 3 <= longer,
            }
    }

    fn clearly_better_than(&self, other: &Match) -> bool {
        if self.distance + 1 < other.distance {
            return true;
        }
        if self.distance == other.distance {
            return self.shared_prefix >= other.shared_prefix + 2 || self.priority < other.priority;
        }
        if self.distance + 1 == other.distance {
            return self.shared_prefix > other.shared_prefix
                || self.priority < other.priority && self.shared_prefix >= other.shared_prefix;
        }
        false
    }
}

/// The most edits a name may be from a query of `query_len` bytes and still
/// be suggested.
fn most_edits(query_len: usize) -> usize {
    match query_len {
        0..=4 => 1,
        5..=7 => 2,
        _ => 3,
    }
}

/// The number of byte insertions, deletions and substitutions that turn `a`
/// into `b`.
fn levenshtein(a: &[u8], b: &[u8]) -> usize {
    let mut previous: Vec<usize> = (0..=b.len()).collect();
    let mut current = vec![0; b.len() + 1];
    for (i, &x) in a.iter().enumerate() {
        current[0] = i + 1;
        for (j, &y) in b.iter().enumerate() {
            let substitution = previous[j] + usize::from(x != y);
            current[j + 1] = substitution.min(previous[j + 1] + 1).min(current[j] + 1);
        }
        std::mem::swap(&mut previous, &mut current);
    }
    previous[b.len()]
}

#[cfg(test)]
mod tests {
    use super::*;

    fn nearest<'a>(query: &str, names: &[&'a str]) -> Option<&'a str> {
        nearest_name(query, names.iter().map(|&name| (name, 0)))
    }

    #[test]
    fn close_typos() {
        assert_eq!(nearest("pritn", &["print"]), Some("print"));
        assert_eq!(nearest("rint", &["print"]), Some("print")); // one edit, no shared prefix
        assert_eq!(nearest("ength", &["length"]), Some("length"));
        assert_eq!(nearest("Lenght", &["length"]), Some("length")); // case is ignored
    }

    #[test]
    fn weak_or_ambiguous_matches_are_rejected() {
        assert_eq!(nearest("foo", &["bar"]), None);
        assert_eq!(nearest("cotn", &["cost", "count"]), None);
        assert_eq!(nearest("", &["a"]), None);
        assert_eq!(nearest("a", &[" "]), None);
        assert_eq!(nearest("ab", &["ab", "AB"]), Some("AB")); // exact short match despite the tie
    }

    #[test]
    fn the_order_of_candidates_does_not_matter() {
        for (query, names) in [
            ("ab", ["aB", "AB", "ab"]),
            ("lenght", ["length", "lent", "height"]),
            ("cotn", ["cost", "count", "coat"]),
        ] {
            let mut reversed = names;
            reversed.reverse();
            assert_eq!(nearest(query, &names), nearest(query, &reversed), "{query}");
        }
    }

    /// Names too much longer or shorter to be suggested are skipped without
    /// changing any answer.
    #[test]
    fn far_lengths_do_not_change_the_answer() {
        let far = ["x", "a_very_long_name_indeed", "lengthy_value_name"];
        for (query, names) in [
            ("lenght", &["length", "lent"][..]),
            ("cotn", &["cost", "count"]),
            ("valeu", &["value"]),
            ("ab", &["ab", "AB"]),
        ] {
            let with_far: Vec<&str> = names.iter().chain(&far).copied().collect();
            assert_eq!(nearest(query, names), nearest(query, &with_far), "{query}");
        }
    }

    #[test]
    fn priority_breaks_a_tie() {
        assert_eq!(nearest_name("foa", [("for", 1), ("foo", 0)]), Some("foo"));
    }

    #[test]
    fn edit_distance() {
        assert_eq!(levenshtein(b"kitten", b"sitting"), 3);
        assert_eq!(levenshtein(b"", b"abc"), 3);
        assert_eq!(levenshtein(b"same", b"same"), 0);
    }
}

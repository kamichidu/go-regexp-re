package ir

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/kamichidu/go-regexp-re/syntax"
)

func BenchmarkPikeVM_Email(b *testing.B) {
	// 一般的な複雑なメールアドレスの正規表現
	pat := `([a-zA-Z0-9_.+-]+)@([a-zA-Z0-9-]+\.[a-zA-Z0-9-.]+)`
	re, _ := syntax.Parse(pat, syntax.Perl)
	prog, _ := syntax.Compile(re)
	numSubexp := re.MaxCap()

	// 負荷を高めるための複雑なメールアドレス10個
	emails := []string{
		"very.very.very.long.local.part.with.many.dots@example.com",
		"a-b-c-d-e-f-g-h-i-j-k-l-m-n-o-p-q-r-s-t-u-v-w-x-y-z@long-domain-name-with-many-hyphens.example.co.jp",
		"user+mailbox+subaddress+very+long+tag+specification@sub.sub.sub.domain.example.com",
		strings.Repeat("a", 64) + "@" + strings.Repeat("b", 64) + ".example.com",
		"1234567890123456789012345678901234567890@example.org",
		"dotted.email.address.with.many.components.in.local@domain.com",
		"mixed-case-and-numbers-123-ABC-xyz-987@some-long-hospital-domain.hospital",
		"____---.---____@long.long.long.long.long.long.long.long.long.long.tld",
		"first.last.middle.suffix.prefix.department.division@company.corporate.global",
		"aaaaaaaaaa.bbbbbbbbbb.cccccccccc.dddddddddd@eeeeeeeeee.ffffffffff.gggggggggg.com",
	}

	for i, email := range emails {
		b.Run(fmt.Sprintf("Case%d", i), func(b *testing.B) {
			input := []byte(email)
			start, end := 0, len(input)

			// Setup trie roots as if coming from DFA compilation
			dfa, _ := NewDFAForSearch(context.Background(), prog)
			trieRoots := dfa.TrieRoots()

			b.ResetTimer()
			for n := 0; n < b.N; n++ {
				res := nfaMatchPikeVM(prog, trieRoots, input, start, end, numSubexp)
				if res == nil {
					b.Fatal("failed to match")
				}
			}
		})
	}
}

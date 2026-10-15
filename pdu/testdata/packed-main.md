# Packed compatibility corpus

Captured from go-smpp `8c6cbba1749a466a6bc3147c7b4153ee02b33f19` before the codec migration on 2026-10-15. The gzip stream contains 12,642 JSON rows in fill, length, pattern order: seven fill offsets 0–6, lengths 0–300 inclusive, and patterns A, @, CR, A ending in @, A ending in CR, and repeated extension-table characters.

Each row records the fill offset (`f`), character count (`n`), pattern (`p`), raw packed bytes as JSON base64 (`w`), inferred decoded text (`d`), and an optional decoder error (`e`). Raw bytes came from public `ShortMessage.SetUDH`, `SetLongMessageWithEnc` and `GetMessageData`; inferred text came from `data.DecodeGSM7Packed` with the same fill and no septet count. This preserves the old padding ambiguity instead of assuming every source text can roundtrip without a septet count. The test also checks full short_message marshal/unmarshal for rows fitting the one-octet wire length.

Uncompressed expectations are intentionally stored rather than regenerated from current production code. The corpus is 143,306 bytes compressed. SHA-256: `575875fda962ba1ba546a0991b6cb4594eaf3b995b6117522deae6fb1f44d478`.

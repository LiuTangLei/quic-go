# Shared QUIC library agreements

- This is the shared QUIC/HTTP3 library for LiuTangLei/tailscale and LiuTangLei/tailcat-quic, not an application repository.
- The owner has disabled GitHub Actions and requested removal of the complete .github directory. Do not restore workflows, schedules, Dependabot or hosted builds during upstream merges.
- Run appropriate local/explicit-host tests before publishing immutable dependency tags. Never rewrite a published tag or use a local module replacement in a release consumer.
- Use lightly tuned BBRv3 as the sole production policy, including zero Config and path migration. Do not reintroduce an algorithm menu; deprecated source-compatibility aliases must still resolve to BBRv3.
- Preserve authentication, encryption, pacing/congestion control, bounded queues and buffer ownership. Report directional WAN results honestly; unit tests are not throughput evidence.
- The development runtime policy is a single default: lightly tuned BBRv3. Do not restore an algorithm-selection menu or accidentally reset migrated connections to Reno/CUBIC. Legacy selector fields/functions may remain only as deprecated source-compatibility shims for Tailscale and Tailcat.
- Keep the public introduction simple ("lightly tuned BBRv3"). Document actual tuning and validation limits in BBRv3.md and the test record; do not equate more aggressive probing with proven universal speed or whole-process CPU gains.
- Tailcat application releases use ordinary versions or quic.N, not h3.N. Library tags may use quic.N; existing tags remain historical immutable references.

# Transparent pricing

This is how tafs works out the credits a run uses. The code is in api/internal/meter.

A run's credits. When a taf's run ends, its AI cost (what its model turns cost, in US dollars) is read in euros at a fixed 1.08 dollars to the euro, then counted at 4 credits per euro cent, rounded up once for the whole run. A run that cost anything uses at least 1 credit. For example, a run whose AI cost $0.27 is €0.25, which is 100 credits.

What a credit costs. On Company, a credit beyond the included ones costs €0.01, so you pay four times the run's AI cost. Solo includes 500 credits a month for €12.50; on Solo, more credits come in packs, from 500 credits for €11.50 (kiff.dev/docs/tafs-credits).

Never counted:
- a trial of a new version, which sends nothing;
- a run that failed on our side;
- Coach, setting your tafs up, within its fair use: about €10 of AI per billing period, about 4 new jobs, after which its runs use credits too.

Counted: a run whose calls KIFF refused, or that you declined, still used its AI, so it uses credits.

Prices, what's included and VAT are on kiff.dev/pricing; the Terms (§8) are the contract.

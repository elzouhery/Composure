# metrics

The fidelity gate's metric history. One JSON object per line, appended by CI on
every push to `main`, never edited.

This branch is orphaned from `main` on purpose. The rows used to be committed to
`main`, which meant CI held write access to the branch every human change has to
be reviewed to reach — so the branch could not be protected without either
breaking the build or exempting Actions from the rule protecting it.

Nothing reads this file back. `cmd/gate` only appends, and the row is uploaded
as a workflow artifact independently. It exists so that a slow downward drift
which never breaches `benchmarks/baseline.json` is still visible as a trend.

The `commit` field carried by 47 of the first 48 rows — the second row has
never had one — refers to commits that no longer exist:
the repository's history was collapsed to a single commit before it was made
public. The measurements are unaffected — the field is a label, not a key
anything resolves.

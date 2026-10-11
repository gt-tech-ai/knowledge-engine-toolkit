# trivy filesystem-scan (vuln + secret) ignore file. One suppression per line:
#
#   <ID> exp:YYYY-MM-DD # reason the suppression is safe until that date
#
# Only a finding with no available fix gets an entry, and every entry carries an
# expiry: an expired exception becomes a finding again, so renew the fix or remove
# the line.
#
# golang.org/x/crypto/openpgp (GO-2026-5932): deprecated, unmaintained, no fixed
# release. govulncheck reports it unreachable and osv-scanner reports 0, but
# trivy's filesystem scan surfaces it from the go.mod graph.
GO-2026-5932 exp:2027-01-02 # x/crypto/openpgp is deprecated with no fix; not imported (git grep x/crypto/openpgp is empty)

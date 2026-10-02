# Imaging operations pack

Operational throughput of a radiology department: how long reports take, how
many exams wait and which scanner is out of service. It answers: *which exams
are waiting behind a scanner that is down, and what would an extra reporting
session or a diversion do to turnaround?*

```bash
./bin/zyntra plan -f packs/imaging-ops
./bin/zyntra ontology impact -f packs/imaging-ops Scanner:biomed:sc-ct2
```

**Scope.** This is capacity and flow. It does not read findings, diagnoses or
patient identifiers, and it is not clinical decision support: no action here
changes how an exam is read or what is concluded. Exports should carry
accession numbers and operational fields only. The sample fixtures contain no
patient data. A deployment in a regulated setting needs its own data-protection
review before connecting real systems.

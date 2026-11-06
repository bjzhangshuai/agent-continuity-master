{{- define "agent-continuity.name" -}}agent-continuity{{- end }}
{{- define "agent-continuity.fullname" -}}{{ .Release.Name | trunc 63 | trimSuffix "-" }}{{- end }}
{{- define "agent-continuity.labels" -}}
app.kubernetes.io/name: {{ include "agent-continuity.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: agent-continuity
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" }}
{{- end }}
{{- define "agent-continuity.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agent-continuity.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/component: server
{{- end }}
{{- define "agent-continuity.serviceAccountName" -}}{{ include "agent-continuity.fullname" . }}{{- end }}
{{- define "agent-continuity.secretChecksum" -}}
{{- $all := dict "revision" .Values.secretRevision -}}
{{- $root := . -}}
{{- range list .Values.existingSecret .Values.databaseTLS.existingSecret .Values.archive.existingSecret .Values.archive.storageSecret .Values.archive.broker.existingSecret .Values.payments.existingSecret .Values.treasury.existingSecret -}}
{{- if . -}}{{- $secret := lookup "v1" "Secret" $root.Release.Namespace . -}}{{- $_ := set $all . ($secret.data | default dict) -}}{{- end -}}
{{- end -}}
{{- toJson $all | sha256sum -}}
{{- end -}}

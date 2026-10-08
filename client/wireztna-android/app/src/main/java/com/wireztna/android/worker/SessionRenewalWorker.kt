package com.wireztna.android.worker

import android.content.Context
import androidx.hilt.work.HiltWorker
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.wireztna.android.data.config.ConfigStore
import com.wireztna.android.data.repository.WireZtnaRepository
import com.wireztna.android.tunnel.TunnelManager
import com.wireztna.android.tunnel.TunnelState
import dagger.assisted.Assisted
import dagger.assisted.AssistedInject
import java.util.concurrent.TimeUnit

/**
 * Periodic worker that renews the session PSK before it expires.
 * Runs every 30 minutes; the session TTL is typically 4-8 hours.
 * After renewal, if the tunnel is active, it reconfigures with the new PSK.
 */
@HiltWorker
class SessionRenewalWorker @AssistedInject constructor(
    @Assisted appContext: Context,
    @Assisted params: WorkerParameters,
    private val repository: WireZtnaRepository,
    private val configStore: ConfigStore,
    private val tunnelManager: TunnelManager,
) : CoroutineWorker(appContext, params) {

    override suspend fun doWork(): Result {
        // Only renew if we're connected
        if (!TunnelState.connected.value) return Result.success()

        // Only renew if we have a token
        if (!configStore.isLoggedIn()) return Result.failure()

        val groupId = configStore.getSelectedGroupId()
        val result = repository.renewSession(groupId = groupId)

        return if (result.isSuccess) {
            // Reconfigure tunnel with the new PSK
            try {
                tunnelManager.reconfigure()
            } catch (_: Exception) {
                // Non-fatal — old PSK still works until expiry
            }
            Result.success()
        } else {
            Result.retry()
        }
    }

    companion object {
        private const val WORK_NAME = "wireztna_session_renewal"

        fun enqueue(context: Context) {
            val request = PeriodicWorkRequestBuilder<SessionRenewalWorker>(
                30, TimeUnit.MINUTES,
                5, TimeUnit.MINUTES,
            ).build()

            WorkManager.getInstance(context).enqueueUniquePeriodicWork(
                WORK_NAME,
                ExistingPeriodicWorkPolicy.KEEP,
                request,
            )
        }

        fun cancel(context: Context) {
            WorkManager.getInstance(context).cancelUniqueWork(WORK_NAME)
        }
    }
}

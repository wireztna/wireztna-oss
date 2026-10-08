package com.wireztna.android.ui.navigation

import androidx.compose.runtime.Composable
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.hilt.navigation.compose.hiltViewModel
import androidx.navigation.compose.NavHost
import androidx.navigation.compose.composable
import androidx.navigation.compose.rememberNavController
import com.wireztna.android.ui.screens.EnrollScreen
import com.wireztna.android.ui.screens.HomeScreen
import com.wireztna.android.ui.screens.LoginScreen
import com.wireztna.android.ui.viewmodel.AppViewModel

sealed class Route(val route: String) {
    data object Enroll : Route("enroll")
    data object Login : Route("login")
    data object Home : Route("home")
}

@Composable
fun WireZtnaNavHost() {
    val navController = rememberNavController()
    val appViewModel: AppViewModel = hiltViewModel()
    val uiState by appViewModel.uiState.collectAsState()

    // Determine start destination based on enrollment/login state
    val startDestination = when {
        !uiState.isEnrolled -> Route.Enroll.route
        !uiState.isLoggedIn -> Route.Login.route
        else -> Route.Home.route
    }

    NavHost(navController = navController, startDestination = startDestination) {
        composable(Route.Enroll.route) {
            EnrollScreen(
                onEnrollSuccess = {
                    navController.navigate(Route.Login.route) {
                        popUpTo(Route.Enroll.route) { inclusive = true }
                    }
                }
            )
        }

        composable(Route.Login.route) {
            LoginScreen(
                onLoginSuccess = {
                    navController.navigate(Route.Home.route) {
                        popUpTo(Route.Login.route) { inclusive = true }
                    }
                }
            )
        }

        composable(Route.Home.route) {
            HomeScreen(
                onLogout = {
                    navController.navigate(Route.Login.route) {
                        popUpTo(Route.Home.route) { inclusive = true }
                    }
                },
                onUnenroll = {
                    navController.navigate(Route.Enroll.route) {
                        popUpTo(Route.Home.route) { inclusive = true }
                    }
                },
            )
        }
    }
}
